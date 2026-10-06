package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/taills/leaf/v2/network"
	leaftransport "github.com/taills/tbcg/server/internal/transport/leaf"
)

func runProbe(getenv func(string) string, stopped <-chan os.Signal) error {
	config, err := loadProbeConfig(getenv)
	if err != nil {
		return err
	}
	certificate := getenv("TBCG_PROTOCOL_PROBE_TLS_CERT")
	privateKey := getenv("TBCG_PROTOCOL_PROBE_TLS_KEY")
	if (certificate == "") != (privateKey == "") {
		return fmt.Errorf("both TBCG_PROTOCOL_PROBE_TLS_CERT and TBCG_PROTOCOL_PROBE_TLS_KEY are required for TLS")
	}

	server := &network.WSServer{
		Addr:            config.address,
		MaxConnNum:      32,
		PendingWriteNum: 32,
		MaxMsgLen:       leaftransport.MaxMessageBytes,
		HTTPTimeout:     10 * time.Second,
		CertFile:        certificate,
		KeyFile:         privateKey,
		CheckOrigin:     probeCheckOrigin,
		NewAgent: func(conn *network.WSConn) network.Agent {
			return leaftransport.NewAgent(conn, leaftransport.NewProcessor(), config.agentConfig())
		},
	}
	server.Start()
	protocol := "ws"
	if certificate != "" {
		protocol = "wss"
	}
	log.Printf("Leaf protocol probe listening at %s://%s", protocol, config.address)
	<-stopped
	server.Close()
	return nil
}
