//go:build !darwin

package ca

type stubDetector struct{}

func newDetector() Detector { return &stubDetector{} }

func (d *stubDetector) CheckAll(fp string) []TrustStatus {
	return nil
}
