// Package oprf provides an implementation of Oblivious Pseudorandom Functions
// (OPRFs), as defined on draft-irtf-cfrg-voprf: https://datatracker.ietf.org/doc/draft-irtf-cfrg-voprf/
// It implements:
// For a Client:
//   - Blind
//   - Unblind
//   - Finalize
//
// For a Server:
//   - Setup
//   - Evaluate
//   - VerifyFinalize
package oprf

import (
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"hash"
	"io"

	"github.com/cloudflare/circl/oprf/group"
)

// SuiteID is a type that represents the ID of a Suite.
type SuiteID uint16

const (
	// OPRFP256 is the constant to represent the OPRF P-256 with SHA-512 (SSWU-RO) group.
	OPRFP256 SuiteID = 0x0003
	// OPRFP384 is the constant to represent the OPRF P-384 with SHA-512 (SSWU-RO) group.
	OPRFP384 SuiteID = 0x0004
	// OPRFP521 is the constant to represent the OPRF P-521 with SHA-512 (SSWU-RO) group.
	OPRFP521 SuiteID = 0x0005
)

var (
	// OPRFMode is the context string to define a OPRF.
	OPRFMode byte = 0x00
)

var (
	// ErrUnsupportedGroup is an error stating that the ciphersuite chosen is not supported
	ErrUnsupportedGroup = errors.New("unsupported group")
)

// BlindToken corresponds to a token that has been blinded.
// Internally, it is a serialized Element.
type BlindToken []byte

// IssuedToken corresponds to a token that has been issued.
// Internally, it is a serialized Element.
type IssuedToken []byte

// Token is the object issuance of the protocol.
type Token struct {
	data  []byte
	blind *group.Scalar
}

// Evaluation corresponds to the evaluation over a token.
type Evaluation struct {
	Element []byte
}

// KeyPair is an struct containing a public and private key.
type KeyPair struct {
	publicKey  *group.Element
	privateKey *group.Scalar
}

// Client is a representation of a Client during protocol execution.
type Client struct {
	suite   *group.Ciphersuite
	context []byte
}

// Server is a representation of a Server during protocol execution.
type Server struct {
	suite   *group.Ciphersuite
	context []byte
	Kp      KeyPair
}

func generateContext(id SuiteID) []byte {
	context := [3]byte{OPRFMode, 0, byte(id)}

	return context[:]
}

// Serialize serializes a KeyPair elements into byte arrays.
func (kp *KeyPair) Serialize() []byte {
	return kp.privateKey.Serialize()
}

// Deserialize deserializes a KeyPair into an element and field element of the group.
func (kp *KeyPair) Deserialize(suite *group.Ciphersuite, encoded []byte) error {
	privateKey := group.NewScalar(suite.Curve)
	privateKey.Deserialize(encoded)
	publicKey := suite.Generator().ScalarBaseMult(privateKey)

	kp.publicKey = publicKey
	kp.privateKey = privateKey

	return nil
}

// GenerateKeyPair generates a KeyPair in accordance with the group.
func GenerateKeyPair(suite *group.Ciphersuite) KeyPair {
	privateKey := suite.RandomScalar()
	publicKey := suite.Generator().ScalarBaseMult(privateKey)

	return KeyPair{publicKey, privateKey}
}

func GroupFromID(id SuiteID, context []byte) (*group.Ciphersuite, error) {
	var err error
	var suite *group.Ciphersuite

	suite, err = group.NewSuite(uint16(id), context)
	if err != nil {
		return nil, err
	}

	return suite, err
}

// NewServer creates a new instantiation of a Server.
func NewServer(id SuiteID) (*Server, error) {
	context := generateContext(id)

	suite, err := GroupFromID(id, context)
	if err != nil {
		return nil, err
	}
	keyPair := GenerateKeyPair(suite)

	return &Server{
		suite:   suite,
		context: context,
		Kp:      keyPair}, nil
}

// NewServerWithKeyPair creates a new instantiation of a Server. It can create
// a server with existing keys or use pre-generated keys.
func NewServerWithKeyPair(id SuiteID, kp KeyPair) (*Server, error) {
	context := generateContext(id)

	suite, err := GroupFromID(id, context)
	if err != nil {
		return nil, err
	}

	return &Server{
		suite:   suite,
		context: context,
		Kp:      kp}, nil
}

// Evaluate blindly signs a client token.
func (s *Server) Evaluate(b BlindToken) (*Evaluation, error) {
	p := group.NewElement(s.suite.Curve)
	err := p.Deserialize(b)
	if err != nil {
		return nil, err
	}

	z := p.ScalarMult(s.Kp.privateKey)
	ser := z.Serialize()

	return &Evaluation{ser}, nil
}

func mustWrite(h io.Writer, data []byte) {
	dataLen, err := h.Write(data)
	if err != nil {
		panic(err)
	}
	if len(data) != dataLen {
		panic("failed to write")
	}
}

// FinalizeHash computes the final hash for the suite.
func finalizeHash(c *group.Ciphersuite, data, iToken, info, context []byte) []byte {
	var h hash.Hash
	if c.Hash == "sha256" {
		h = sha256.New()
	} else if c.Hash == "sha512" {
		h = sha512.New()
	}

	lenBuf := make([]byte, 2)

	binary.BigEndian.PutUint16(lenBuf, uint16(len(data)))
	mustWrite(h, lenBuf)
	mustWrite(h, data)

	binary.BigEndian.PutUint16(lenBuf, uint16(len(iToken)))
	mustWrite(h, lenBuf)
	mustWrite(h, iToken)

	binary.BigEndian.PutUint16(lenBuf, uint16(len(info)))
	mustWrite(h, lenBuf)
	mustWrite(h, info)

	dst := []byte("VOPRF05-Finalize-")
	dst = append(dst, context...)

	binary.BigEndian.PutUint16(lenBuf, uint16(len(dst)))
	mustWrite(h, lenBuf)
	mustWrite(h, dst)

	return h.Sum(nil)
}

// FullEvaluate performs a full evaluation at the server side.
func (s *Server) FullEvaluate(in, info []byte) ([]byte, error) {
	p, err := s.suite.HashToGroup(in)
	if err != nil {
		return nil, err
	}

	t := p.ScalarMult(s.Kp.privateKey)
	iToken := t.Serialize()

	h := finalizeHash(s.suite, in, iToken, info, s.context)

	return h, nil
}

// VerifyFinalize verifies the evaluation.
func (s *Server) VerifyFinalize(in, info, out []byte) bool {
	p, err := s.suite.HashToGroup(in)
	if err != nil {
		return false
	}

	el := p.Serialize()

	e, err := s.Evaluate(el)
	if err != nil {
		return false
	}

	h := finalizeHash(s.suite, in, e.Element, info, s.context)
	return subtle.ConstantTimeCompare(h, out) == 1
}

// NewClient creates a new instantiation of a Client.
func NewClient(id SuiteID) (*Client, error) {
	context := generateContext(id)

	suite, err := GroupFromID(id, context)
	if err != nil {
		return nil, err
	}

	return &Client{
		suite:   suite,
		context: context}, nil
}

// ClientRequest is a structure to encapsulate the output of a Request call.
type ClientRequest struct {
	suite        *group.Ciphersuite
	context      []byte
	token        *Token
	BlindedToken BlindToken
}

// Request generates a token and its blinded version.
func (c *Client) Request(in []byte) (*ClientRequest, error) {
	r := c.suite.RandomScalar()

	p, err := c.suite.HashToGroup(in)
	if err != nil {
		return nil, err
	}

	t := p.ScalarMult(r)
	BlindedToken := t.Serialize()

	tk := &Token{in, r}
	return &ClientRequest{c.suite, c.context, tk, BlindedToken}, nil
}

// Finalize computes the signed token from the server Evaluation and returns
// the output of the OPRF protocol.
func (cr *ClientRequest) Finalize(e *Evaluation, info []byte) ([]byte, error) {
	p := group.NewElement(cr.suite.Curve)
	err := p.Deserialize(e.Element)
	if err != nil {
		return nil, err
	}

	r := cr.token.blind
	rInv := r.Inv()

	tt := p.ScalarMult(rInv)
	iToken := tt.Serialize()

	h := finalizeHash(cr.suite, cr.token.data, iToken, info, cr.context)
	return h, nil
}
