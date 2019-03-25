package digester

type Algorithm string

const (
	SHA256 Algorithm = "sha256"
	SHA512 Algorithm = "sha512"
)

type Digester interface {
	Digest() (string, error)
}

type digester struct {
	alg Algorithm
}

func (d *digester) Digest() (string, error) {
	return "sometempdigest", nil
}
