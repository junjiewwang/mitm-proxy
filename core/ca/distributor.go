package ca

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
)

type defaultDistributor struct{}

func newDistributor() Distributor { return &defaultDistributor{} }

func (d *defaultDistributor) MobileConfig(caPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return nil, fmt.Errorf("invalid CA PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA: %w", err)
	}

	der := base64.StdEncoding.EncodeToString(cert.Raw)

	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadCertificateFileName</key>
			<string>mitm-proxy-ca.crt</string>
			<key>PayloadContent</key>
			<data>%s</data>
			<key>PayloadDescription</key>
			<string>MITM proxy CA certificate for HTTPS debugging</string>
			<key>PayloadDisplayName</key>
			<string>mitm-proxy Root CA</string>
			<key>PayloadIdentifier</key>
			<string>com.mitm-proxy.ca</string>
			<key>PayloadType</key>
			<string>com.apple.security.root</string>
			<key>PayloadUUID</key>
			<string>mitm-proxy-ca-001</string>
			<key>PayloadVersion</key>
			<integer>1</integer>
		</dict>
	</array>
	<key>PayloadDescription</key>
	<string>Install mitm-proxy Root CA to trust HTTPS interception</string>
	<key>PayloadDisplayName</key>
	<string>mitm-proxy CA</string>
	<key>PayloadIdentifier</key>
	<string>com.mitm-proxy</string>
	<key>PayloadType</key>
	<string>Configuration</string>
	<key>PayloadUUID</key>
	<string>mitm-proxy-profile-001</string>
	<key>PayloadVersion</key>
	<integer>1</integer>
</dict>
</plist>`, der)), nil
}
