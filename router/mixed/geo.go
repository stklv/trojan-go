package mixed

import (
	"net"
	"regexp"
	"strings"

	"github.com/golang/protobuf/proto"
	"github.com/p4gefau1t/trojan-go/common"
	"github.com/p4gefau1t/trojan-go/log"
	"github.com/p4gefau1t/trojan-go/protocol"
	"github.com/p4gefau1t/trojan-go/router"
	v2router "v2ray.com/core/app/router"
)

type GeoRouter struct {
	domains        []*v2router.Domain
	cidrs          []*v2router.CIDR
	matchPolicy    router.Policy
	nonMatchPolicy router.Policy
	strategy       router.Strategy
}

func (r *GeoRouter) matchDomain(fulldomain string) bool {
	for _, d := range r.domains {
		switch d.GetType() {
		case v2router.Domain_Domain, v2router.Domain_Full:
			domain := d.GetValue()
			if strings.HasSuffix(fulldomain, domain) {
				idx := strings.Index(fulldomain, domain)
				if idx == 0 || fulldomain[idx-1] == '.' {
					log.Trace("Domain:", fulldomain, "hit domain rule:", domain)
					return true
				}
			}
		case v2router.Domain_Plain:
			//keyword
			if strings.Contains(fulldomain, d.GetValue()) {
				log.Trace("Domain:", fulldomain, "hit keyword rule:", d.GetValue())
				return true
			}
		case v2router.Domain_Regex:
			matched, err := regexp.Match(d.GetValue(), []byte(fulldomain))
			if err != nil {
				log.Error("Invalid regex", d.GetValue())
				return false
			}
			if matched {
				log.Trace("Domain:", fulldomain, "hit regex rule:", d.GetValue())
				return true
			}
		default:
			log.Debug("Unknown rule type:" + d.GetType().String())
		}
	}
	return false
}

func (r *GeoRouter) matchIP(ip net.IP) bool {
	isIPv6 := true
	len := net.IPv6len
	if ip.To4() != nil {
		len = net.IPv4len
		isIPv6 = false
	}
	for _, c := range r.cidrs {
		n := int(c.GetPrefix())
		mask := net.CIDRMask(n, 8*len)
		cidrIP := net.IP(c.GetIp())
		if cidrIP.To4() != nil { //IPv4 CIDR
			if isIPv6 {
				continue
			}
		} else { //IPv6 CIDR
			if !isIPv6 {
				continue
			}
		}
		subnet := &net.IPNet{IP: cidrIP.Mask(mask), Mask: mask}
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}

func (r *GeoRouter) routeRequestByIP(domain string) (router.Policy, error) {
	addr, err := net.ResolveIPAddr("ip", domain)
	if err != nil {
		return router.Unknown, err
	}
	atype := common.IPv6
	if addr.IP.To4() != nil {
		atype = common.IPv4
	}
	return r.RouteRequest(&protocol.Request{
		Address: &common.Address{
			IP:          addr.IP,
			AddressType: atype,
		},
	})
}

func (r *GeoRouter) RouteRequest(req *protocol.Request) (router.Policy, error) {
	switch req.AddressType {
	case common.DomainName:
		if r.domains == nil {
			return r.nonMatchPolicy, nil
		}
		domain := string(req.DomainName)
		if r.strategy == router.IPOnDemand {
			return r.routeRequestByIP(domain)
		}
		if r.matchDomain(domain) {
			return r.matchPolicy, nil
		}
		if r.strategy == router.IPIfNonMatch {
			return r.routeRequestByIP(domain)
		}
		return r.nonMatchPolicy, nil
	case common.IPv4, common.IPv6:
		if r.cidrs == nil {
			return r.nonMatchPolicy, nil
		}
		if r.matchIP(req.IP) {
			return r.matchPolicy, nil
		}
		return r.nonMatchPolicy, nil
	default:
		return router.Unknown, common.NewError("invalid address type")
	}
}

func (r *GeoRouter) LoadGeoData(geoipData []byte, ipCode []string, geositeData []byte, siteCode []string) error {
	geoip := new(v2router.GeoIPList)
	if err := proto.Unmarshal(geoipData, geoip); err != nil {
		return err
	}
	for _, c := range ipCode {
		c = strings.ToUpper(c)
		found := false
		for _, e := range geoip.GetEntry() {
			code := e.GetCountryCode()
			if c == code {
				r.cidrs = append(r.cidrs, e.GetCidr()...)
				found = true
				break
			}
		}
		if found {
			log.Info("GeoIP tag", c, "loaded")
		} else {
			log.Warn("GeoIP tag", c, "not found")
		}
	}

	geosite := new(v2router.GeoSiteList)
	if err := proto.Unmarshal(geositeData, geosite); err != nil {
		return err
	}
	for _, c := range siteCode {
		c = strings.ToUpper(c)
		found := false
		for _, s := range geosite.GetEntry() {
			code := s.GetCountryCode()
			if c == code {
				domainList := s.GetDomain()
				r.domains = append(r.domains, domainList...)
				found = true
				break
			}
		}
		if found {
			log.Info("GeoSite tag", c, "loaded")
		} else {
			log.Warn("GeoSite tag", c, "not found")
		}
	}
	return nil
}

func NewGeoRouter(matchPolicy router.Policy, nonMatchPolicy router.Policy, strategy router.Strategy) (*GeoRouter, error) {
	r := GeoRouter{
		matchPolicy:    matchPolicy,
		nonMatchPolicy: nonMatchPolicy,
		strategy:       strategy,
	}
	return &r, nil
}
