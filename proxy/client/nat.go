// +build linux

package client

import (
	"context"
	"net"
	"time"

	"github.com/p4gefau1t/trojan-go/common"
	"github.com/p4gefau1t/trojan-go/conf"
	"github.com/p4gefau1t/trojan-go/log"
	"github.com/p4gefau1t/trojan-go/protocol"
	"github.com/p4gefau1t/trojan-go/protocol/tproxy"
	"github.com/p4gefau1t/trojan-go/protocol/trojan"
	"github.com/p4gefau1t/trojan-go/proxy"
	"github.com/p4gefau1t/trojan-go/stat"
)

type NAT struct {
	config        *conf.GlobalConfig
	ctx           context.Context
	cancel        context.CancelFunc
	inboundPacket protocol.PacketSession
	listener      net.Listener
	auth          stat.Authenticator
	appMan        *AppManager
}

func (n *NAT) handleConn(conn net.Conn) {
	inboundConn, req, err := tproxy.NewInboundConnSession(conn)
	if err != nil {
		log.Error(common.NewError("Failed to start inbound session").Base(err))
		return
	}
	defer inboundConn.Close()
	outboundConn, err := n.appMan.OpenAppConn(req)
	if err != nil {
		log.Error(err)
		return
	}
	defer outboundConn.Close()
	log.Info("[Tproxy] conn from", conn.RemoteAddr(), "tunneling to", req)
	proxy.RelayConn(n.ctx, inboundConn, outboundConn, n.config.BufferSize)
}

func (n *NAT) listenUDP(errChan chan error) {
	inboundPacket, err := tproxy.NewInboundPacketSession(n.ctx, n.config)
	if err != nil {
		errChan <- err
		return
	}
	n.inboundPacket = inboundPacket
	defer inboundPacket.Close()
	req := &protocol.Request{
		Address: &common.Address{
			DomainName:  "UDP_CONN",
			AddressType: common.DomainName,
		},
		Command: protocol.Associate,
	}
	for {
		outboundConn, err := n.appMan.OpenAppConn(req)
		if err != nil {
			log.Error(err)
			time.Sleep(time.Second)
			continue
		}
		outboundPacket, err := trojan.NewPacketSession(outboundConn)
		common.Must(err)
		proxy.RelayPacket(n.ctx, inboundPacket, outboundPacket)
		outboundPacket.Close()
	}
}

func (n *NAT) listenTCP(errChan chan error) {
	listener, err := net.Listen("tcp", n.config.LocalAddress.String())
	if err != nil {
		errChan <- err
		return
	}
	n.listener = listener
	defer listener.Close()
	for {
		conn, err := n.listener.Accept()
		if err != nil {
			select {
			case <-n.ctx.Done():
				return
			default:
			}
			errChan <- err
			break
		}
		go n.handleConn(conn)
	}
}

func (n *NAT) Run() error {
	log.Info("Trojan-Go NAT is listening on", n.config.LocalAddress)
	errChan := make(chan error, 2)
	go n.listenUDP(errChan)
	go n.listenTCP(errChan)
	select {
	case err := <-errChan:
		return err
	case <-n.ctx.Done():
		return nil
	}
}

func (n *NAT) Close() error {
	log.Info("Shutting down NAT...")
	n.cancel()
	if n.listener != nil {
		n.listener.Close()
	}
	if n.inboundPacket != nil {
		n.inboundPacket.Close()
	}
	return nil
}

func (n *NAT) Build(config *conf.GlobalConfig) (common.Runnable, error) {
	ctx, cancel := context.WithCancel(context.Background())
	auth, err := stat.NewAuth(ctx, "memory", config)
	if err != nil {
		cancel()
		return nil, err
	}
	appMan := NewAppManager(ctx, config, auth)

	newNAT := &NAT{
		ctx:    ctx,
		cancel: cancel,
		config: config,
		auth:   auth,
		appMan: appMan,
	}
	return newNAT, nil
}

func init() {
	proxy.RegisterProxy(conf.NAT, &NAT{})
}
