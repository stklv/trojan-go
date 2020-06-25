package easy

import (
	"flag"
	"fmt"
	"net"

	"github.com/p4gefau1t/trojan-go/option"

	"github.com/p4gefau1t/trojan-go/common"
	"github.com/p4gefau1t/trojan-go/log"
	"github.com/p4gefau1t/trojan-go/proxy"
)

type easy struct {
	server   *bool
	client   *bool
	password *string
	local    *string
	remote   *string
	cert     *string
	key      *string
}

func (o *easy) Name() string {
	return "easy"
}

func (o *easy) Handle() error {
	if !*o.server && !*o.client {
		return common.NewError("empty")
	}
	if *o.password == "" {
		log.Fatal("empty password is not allowed")
	}
	log.Info("easy mode enabled, trojan-go will NOT use the config file")
	if *o.client {
		clientConfigFormat := `
{
    "run_type": "client",
    "local_addr": "%s",
    "local_port": %s,
    "remote_addr": "%s",
    "remote_port": %s,
    "password": [
        "%s"
    ]
}
		`
		if *o.local == "" {
			log.Warn("client local addr is unspecified, using 127.0.0.1:1080")
			*o.local = "127.0.0.1:1080"
		}
		localHost, localPort, err := net.SplitHostPort(*o.local)
		if err != nil {
			log.Fatal(common.NewError("invalid local addr format:" + *o.local).Base(err))
		}
		remoteHost, remotePort, err := net.SplitHostPort(*o.remote)
		if err != nil {
			log.Fatal(common.NewError("invalid remote addr format:" + *o.remote).Base(err))
		}
		clientConfigJSON := fmt.Sprintf(clientConfigFormat, localHost, localPort, remoteHost, remotePort, *o.password)
		log.Info("generated json config:")
		log.Info(clientConfigJSON)
		proxy, err := proxy.NewProxyFromConfigData([]byte(clientConfigJSON), true)
		if err != nil {
			log.Fatal(err)
		}
		if err := proxy.Run(); err != nil {
			log.Fatal(err)
		}

	} else if *o.server {
		serverConfigFormat := `
{
    "run_type": "server",
    "local_addr": "%s",
    "local_port": %s,
    "remote_addr": "%s",
    "remote_port": %s,
    "password": [
        "%s"
    ],
    "ssl": {
        "verify_hostname": false,
        "cert": "%s",
        "key": "%s"
    }
}
		`
		if *o.remote == "" {
			log.Warn("server remote addr is unspecified, using 127.0.0.1:80")
			*o.remote = "127.0.0.1:80"
		}
		if *o.local == "" {
			log.Warn("server local addr is unspecified, using 0.0.0.0:443")
			*o.local = "0.0.0.0:443"
		}
		localHost, localPort, err := net.SplitHostPort(*o.local)
		if err != nil {
			log.Fatal(common.NewError("invalid local addr format:" + *o.local).Base(err))
		}
		remoteHost, remotePort, err := net.SplitHostPort(*o.remote)
		if err != nil {
			log.Fatal(common.NewError("invalid remote addr format:" + *o.remote).Base(err))
		}
		serverConfigJSON := fmt.Sprintf(serverConfigFormat, localHost, localPort, remoteHost, remotePort, *o.password, *o.cert, *o.key)
		log.Info("generated json config:")

		log.Info(serverConfigJSON)
		proxy, err := proxy.NewProxyFromConfigData([]byte(serverConfigJSON), true)
		if err != nil {
			log.Fatal(err)
		}
		if err := proxy.Run(); err != nil {
			log.Fatal(err)
		}
	}
	return nil
}

func (o *easy) Priority() int {
	return 50
}

func init() {
	option.RegisterHandler(&easy{
		server:   flag.Bool("server", false, "Run a trojan-go server"),
		client:   flag.Bool("client", false, "Run a trojan-go client"),
		password: flag.String("password", "", "Password for authentication"),
		remote:   flag.String("remote", "", "Remote address, e.g. 127.0.0.1:12345"),
		local:    flag.String("local", "", "Local address, e.g. 127.0.0.1:12345"),
		key:      flag.String("key", "server.key", "Key of the server"),
		cert:     flag.String("cert", "server.crt", "Certificates of the server"),
	})
}
