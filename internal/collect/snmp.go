package collect

import (
	"context"
	"fmt"
	"time"

	"github.com/gosnmp/gosnmp"
)

// SNMPConfig describes read-only access. Credentials are supplied through the
// collector's environment; this type is never logged or sent to the API.
type SNMPConfig struct {
	Target          string
	Port            uint16
	Version         string
	Community       string
	User            string
	AuthPassword    string
	PrivacyPassword string
}

func ConnectSNMP(ctx context.Context, config SNMPConfig) (*gosnmp.GoSNMP, error) {
	client := &gosnmp.GoSNMP{
		Target:  config.Target,
		Port:    config.Port,
		Timeout: 3 * time.Second,
		Retries: 1,
		Context: ctx,
	}
	switch config.Version {
	case "2c":
		if config.Community == "" {
			return nil, fmt.Errorf("SNMP_COMMUNITY is required for SNMP v2c")
		}
		client.Version = gosnmp.Version2c
		client.Community = config.Community
	case "3":
		if config.User == "" || config.AuthPassword == "" || config.PrivacyPassword == "" {
			return nil, fmt.Errorf("SNMP_USER, SNMP_AUTH_PASSWORD, and SNMP_PRIV_PASSWORD are required for SNMP v3")
		}
		client.Version = gosnmp.Version3
		client.SecurityModel = gosnmp.UserSecurityModel
		client.MsgFlags = gosnmp.AuthPriv
		client.SecurityParameters = &gosnmp.UsmSecurityParameters{
			UserName:                 config.User,
			AuthenticationProtocol:   gosnmp.SHA256,
			AuthenticationPassphrase: config.AuthPassword,
			PrivacyProtocol:          gosnmp.AES,
			PrivacyPassphrase:        config.PrivacyPassword,
		}
	default:
		return nil, fmt.Errorf("SNMP version must be 3 or 2c")
	}
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("SNMP connection: %w", err)
	}
	return client, nil
}
