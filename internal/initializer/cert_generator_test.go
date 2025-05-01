package initializer

import "crypto/x509"

// MockCertificateGenerator is used to mock certificate generator because the
// real one takes a few seconds to generate a real certificate.
type MockCertificateGenerator struct {
	MockGenerate func(*x509.Certificate, *CertificateSigner) (key []byte, crt []byte, err error)
	MockParse    func([]byte) (*x509.Certificate, error)
}

// Generate calls MockGenerate.
func (m *MockCertificateGenerator) Generate(cert *x509.Certificate, signer *CertificateSigner) (key []byte, crt []byte, err error) {
	return m.MockGenerate(cert, signer)
}

// Parse calls MockParse.
func (m *MockCertificateGenerator) Parse(crt []byte) (*x509.Certificate, error) {
	return m.MockParse(crt)
}
