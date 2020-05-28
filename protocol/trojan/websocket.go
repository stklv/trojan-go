package trojan

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/p4gefau1t/trojan-go/common"
	"github.com/p4gefau1t/trojan-go/conf"
	"github.com/p4gefau1t/trojan-go/log"
	"github.com/p4gefau1t/trojan-go/protocol"
	"github.com/p4gefau1t/trojan-go/shadow"
	"golang.org/x/net/websocket"
)

//this AES layer is used for obfuscation purpose only
type obfReadWriteCloser struct {
	net.Conn
	r     cipher.StreamReader
	w     cipher.StreamWriter
	bufrw *bufio.ReadWriter
}

func (rwc *obfReadWriteCloser) Read(p []byte) (int, error) {
	return rwc.r.Read(p)
}

func (rwc *obfReadWriteCloser) Write(p []byte) (int, error) {
	n, err := rwc.w.Write(p)
	rwc.bufrw.Flush()
	return n, err
}

func (rwc *obfReadWriteCloser) Close() error {
	return rwc.Conn.Close()
}

func NewOutboundObfReadWriteCloser(key []byte, conn net.Conn) *obfReadWriteCloser {
	// use bufio to avoid fixed ws packet length
	bufrw := common.NewBufioReadWriter(conn)
	iv := [aes.BlockSize]byte{}
	common.Must2(io.ReadFull(rand.Reader, iv[:]))
	bufrw.Write(iv[:])
	log.Debug("obfs sent iv", iv)

	block, err := aes.NewCipher(key)
	common.Must(err)

	return &obfReadWriteCloser{
		r: cipher.StreamReader{
			S: cipher.NewCTR(block, iv[:]),
			R: bufrw,
		},
		w: cipher.StreamWriter{
			S: cipher.NewCTR(block, iv[:]),
			W: bufrw,
		},
		Conn:  conn,
		bufrw: bufrw,
	}
}

func NewInboundObfReadWriteCloser(key []byte, conn net.Conn) (*obfReadWriteCloser, error) {
	bufrw := common.NewBufioReadWriter(conn)
	iv := [aes.BlockSize]byte{}
	_, err := bufrw.Read(iv[:])
	if err != nil {
		return nil, err
	}
	log.Debug("obfs recv iv", iv)

	block, err := aes.NewCipher(key)
	common.Must(err)

	return &obfReadWriteCloser{
		r: cipher.StreamReader{
			S: cipher.NewCTR(block, iv[:]),
			R: bufrw,
		},
		w: cipher.StreamWriter{
			S: cipher.NewCTR(block, iv[:]),
			W: bufrw,
		},
		Conn:  conn,
		bufrw: bufrw,
	}, nil
}

//Fake response writer
//Websocket ServeHTTP method uses its Hijack method to get the Readwriter
type wsHttpResponseWriter struct {
	http.Hijacker
	http.ResponseWriter

	ReadWriter *bufio.ReadWriter
	Conn       net.Conn
}

func (w *wsHttpResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.Conn, w.ReadWriter, nil
}

// TODO wrap this with a struct
var tlsSessionCache = tls.NewLRUClientSessionCache(-1)

func NewOutboundWebosocket(conn net.Conn, config *conf.GlobalConfig) (io.ReadWriteCloser, error) {
	url := "wss://" + config.Websocket.HostName + config.Websocket.Path
	origin := "https://" + config.Websocket.HostName
	wsConfig, err := websocket.NewConfig(url, origin)
	if err != nil {
		return nil, err
	}
	wsConn, err := websocket.NewClient(wsConfig, conn)
	if err != nil {
		return nil, err
	}
	var transport net.Conn = wsConn
	if config.Websocket.ObfuscationPassword != "" {
		log.Debug("ws obfs enabled")
		transport = NewOutboundObfReadWriteCloser(config.Websocket.ObfuscationKey, wsConn)
	}
	if !config.Websocket.DoubleTLS {
		return transport, nil
	}
	log.Debug("ws double tls enabled")
	tlsConfig := &tls.Config{
		CipherSuites:           config.Websocket.TLS.CipherSuites,
		RootCAs:                config.Websocket.TLS.CertPool,
		ServerName:             config.Websocket.TLS.SNI,
		SessionTicketsDisabled: !config.Websocket.TLS.SessionTicket,
		InsecureSkipVerify:     !config.Websocket.TLS.Verify,
		ClientSessionCache:     tlsSessionCache,
	}
	tlsConn := tls.Client(transport, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		return nil, err
	}
	if config.LogLevel == 0 {
		state := tlsConn.ConnectionState()
		chain := state.VerifiedChains
		log.Trace("Websocket double TLS handshaked", "cipher:", tls.CipherSuiteName(state.CipherSuite), "resume:", state.DidResume)
		for i := range chain {
			for j := range chain[i] {
				log.Trace("Subject:", chain[i][j].Subject, "Issuer:", chain[i][j].Issuer)
			}
		}
	}
	return tlsConn, nil
}

func dialToWebosocketServer(config *conf.GlobalConfig, url, origin string) (*websocket.Conn, error) {
	wsConfig, err := websocket.NewConfig(url, origin)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("tcp", config.RemoteAddress.String())
	if err != nil {
		return nil, err
	}
	newWsConn, err := websocket.NewClient(wsConfig, conn)
	if err != nil {
		return nil, err
	}
	return newWsConn, nil
}

func getWebsocketScapegoat(config *conf.GlobalConfig, url, origin, info string, conn net.Conn) (*shadow.Scapegoat, error) {
	shadowConn, err := dialToWebosocketServer(config, url, origin)
	if err != nil {
		return nil, err
	}
	return &shadow.Scapegoat{
		Conn:       conn,
		ShadowConn: shadowConn,
		Info:       info,
	}, nil
}

func NewInboundWebsocket(ctx context.Context, conn net.Conn, config *conf.GlobalConfig, shadowMan *shadow.ShadowManager) (io.ReadWriteCloser, error) {
	rewindConn := common.NewRewindConn(conn)
	rewindConn.R.SetBufferSize(512)
	defer rewindConn.R.StopBuffering()

	bufrw := bufio.NewReadWriter(bufio.NewReader(rewindConn), bufio.NewWriter(rewindConn))
	httpRequest, err := http.ReadRequest(bufrw.Reader)
	if err != nil {
		log.Debug(common.NewError("Not a http request:").Base(err))
		return nil, nil
	}

	//this is a http request
	if httpRequest.URL.Path != config.Websocket.Path || //check url path
		strings.ToLower(httpRequest.Header.Get("Upgrade")) != "websocket" { //check upgrade field
		//not a valid websocket conn
		rewindConn.R.Rewind()
		err := common.NewError("Invalid websocket request from " + conn.RemoteAddr().String())
		shadowMan.SubmitScapegoat(&shadow.Scapegoat{
			Conn:          rewindConn,
			ShadowAddress: config.RemoteAddress,
			Info:          err.Error(),
		})
		return nil, err
	}

	//this is a websocket upgrade request
	//no need to record the recv content for now
	rewindConn.R.SetBufferSize(0)
	url := "wss://" + config.Websocket.HostName + config.Websocket.Path
	origin := "https://" + config.Websocket.HostName
	wsConfig, err := websocket.NewConfig(url, origin)

	handshaked := make(chan struct{})

	var wsConn *websocket.Conn
	wsServer := websocket.Server{
		Config: *wsConfig,
		Handler: func(conn *websocket.Conn) {
			wsConn = conn //store the websocket after handshaking
			log.Debug("websocket obtained")
			handshaked <- struct{}{}
			//this function will NOT return unless the connection is ended
			//or the websocket will be closed by ServeHTTP method
			<-ctx.Done()
			log.Debug("websocket closed")
		},
		Handshake: func(wsConfig *websocket.Config, httpRequest *http.Request) error {
			log.Debug("websocket url", httpRequest.URL, "origin", httpRequest.Header.Get("Origin"))
			return nil
		},
	}

	responseWriter := &wsHttpResponseWriter{
		Conn:       conn,
		ReadWriter: bufrw,
	}
	go wsServer.ServeHTTP(responseWriter, httpRequest)

	select {
	case <-handshaked:
	case <-time.After(protocol.TCPTimeout):
	}

	if wsConn == nil {
		//conn has been closed at this point
		return nil, common.NewError("failed to perform websocket handshake")
	}

	//use ws to transfer
	var transport net.Conn
	rewindConn = common.NewRewindConn(wsConn)
	transport = rewindConn

	//start buffering the websocket payload
	rewindConn.R.SetBufferSize(512)
	defer rewindConn.R.StopBuffering()

	if config.Websocket.ObfuscationPassword != "" {
		log.Debug("ws obfs")

		//deadline for sending the iv and hash
		protocol.SetRandomizedTimeout(rewindConn)
		transport, err = NewInboundObfReadWriteCloser(config.Websocket.ObfuscationKey, transport)
		protocol.CancelTimeout(rewindConn)

		if err != nil {
			rewindConn.R.Rewind()
			//redirect this to our own ws server
			err = common.NewError("Remote websocket " + conn.RemoteAddr().String() + "didn't send any valid iv").Base(err)
			goat, err := getWebsocketScapegoat(
				config,
				url,
				origin,
				err.Error(),
				rewindConn,
			)
			if err != nil {
				log.Error(common.NewError("Failed to obtain websocket scapegoat").Base(err))
				wsConn.WriteClose(500)
			} else {
				shadowMan.SubmitScapegoat(goat)
			}
			return nil, err
		}
	}
	if !config.Websocket.DoubleTLS {
		rewindConn.R.SetBufferSize(0)
		return transport, nil
	}
	tlsConfig := &tls.Config{
		Certificates:             config.Websocket.TLS.KeyPair,
		CipherSuites:             config.Websocket.TLS.CipherSuites,
		PreferServerCipherSuites: config.Websocket.TLS.PreferServerCipher,
		SessionTicketsDisabled:   !config.Websocket.TLS.SessionTicket,
	}
	tlsConn := tls.Server(transport, tlsConfig)
	protocol.SetRandomizedTimeout(tlsConn)
	if tlsErr := tlsConn.Handshake(); tlsErr != nil {
		rewindConn.R.Rewind()
		rewindConn.R.StopBuffering()
		//proxy this to our own ws server
		tlsErr = common.NewError("Invalid double TLS handshake from " + conn.RemoteAddr().String()).Base(tlsErr)
		goat, err := getWebsocketScapegoat(
			config,
			url,
			origin,
			tlsErr.Error(),
			rewindConn,
		)
		if err != nil {
			log.Error(common.NewError("Failed to obtain websocket scapegoat").Base(err))
			wsConn.WriteClose(500)
		} else {
			shadowMan.SubmitScapegoat(goat)
		}
		return nil, tlsErr
	}
	protocol.CancelTimeout(tlsConn)
	rewindConn.R.SetBufferSize(0)
	return tlsConn, nil
}
