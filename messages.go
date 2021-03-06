package mls

import (
	"bytes"
	"fmt"
	"github.com/bifurcation/mint/syntax"
)

// struct {
//     CipherSuite cipher_suites<0..255>;
//     DHPublicKey pre_key;
//     SignaturePublicKey identity_key;
//     SignatureScheme algorithm;
//     opaque signature<0..2^16-1>;
// } UserPreKey;
//
// TODO(rlb@ipv.sx): Add credentials
// TODO(rlb@ipv.sx): Crypto agility
type UserPreKey struct {
	PreKey      DHPublicKey
	IdentityKey SignaturePublicKey
	Signature   []byte `tls:"head=2"`
}

func NewUserPreKey(identityKey SignaturePrivateKey) (priv DHPrivateKey, upk *UserPreKey, err error) {
	priv = NewDHPrivateKey()
	upk = &UserPreKey{
		PreKey:      priv.PublicKey,
		IdentityKey: identityKey.PublicKey,
		Signature:   nil,
	}

	tbs, err := syntax.Marshal(upk)
	if err != nil {
		return
	}

	// Strip the signature header octets
	tbs = tbs[:len(tbs)-2]

	upk.Signature = identityKey.sign(tbs)
	return
}

func (upk UserPreKey) Verify() error {
	tbs, err := syntax.Marshal(upk)
	if err != nil {
		return err
	}
	tbs = tbs[:len(tbs)-len(upk.Signature)-2]

	if !upk.IdentityKey.verify(tbs, upk.Signature) {
		return fmt.Errorf("Invalid signature")
	}

	return nil
}

// struct {
//     uint32 epoch;
//     uint32 group_size;
//     opaque group_id<0..2^16-1>;
//     CipherSuite cipher_suite;
//     DHPublicKey update_key;
//     MerkleNode identity_frontier<0..2^16-1>;
//     DHPublicKey ratchet_frontier<0..2^16-1>;
// } GroupPreKey;
type GroupPreKey struct {
	Epoch            uint32
	GroupID          []byte `tls:"head=2"`
	GroupSize        uint32
	UpdateKey        DHPublicKey
	IdentityFrontier MerklePath `tls:"min=1,head=2"`
	RatchetFrontier  DHPath     `tls:"min=1,head=2"`
}

///
/// Handshake Bodies
///

type HandshakeType uint8

const (
	HandshakeTypeNone     HandshakeType = 0
	HandshakeTypeUserAdd  HandshakeType = 1
	HandshakeTypeGroupAdd HandshakeType = 2
	HandshakeTypeUpdate   HandshakeType = 3
	HandshakeTypeDelete   HandshakeType = 4
)

type HandshakeMessageBody interface {
	Type() HandshakeType
}

// struct {} None;
type None struct{}

func (n None) Type() HandshakeType {
	return HandshakeTypeNone
}

// TODO(rlb@ipv.sx): Introduce Init message

// struct {
//     DHPublicKey add_path<1..2^16-1>;
// } UserAdd;
type UserAdd struct {
	AddPath []DHPublicKey `tls:"min=1,head=2"`
}

func (ua UserAdd) Type() HandshakeType {
	return HandshakeTypeUserAdd
}

// struct {
//     UserPreKey pre_key;
// } GroupAdd;
type GroupAdd struct {
	PreKey UserPreKey
}

func (ga GroupAdd) Type() HandshakeType {
	return HandshakeTypeGroupAdd
}

// struct {
//     DHPublicKey path<1..2^16-1>;
// } Update;
type Update struct {
	Path DHPath `tls:"min=1,head=2"`
}

func (u Update) Type() HandshakeType {
	return HandshakeTypeUpdate
}

// struct {
//     uint32 deleted;
//     DHPublicKey path<1..2^16-1>;
// } Delete;
type Delete struct {
	Deleted uint32
	Path    DHPath `tls:"min=1,head=2"`
}

func (d Delete) Type() HandshakeType {
	return HandshakeTypeDelete
}

// struct {
//     HandshakeType msg_type;
//     uint24 inner_length;
//     select (Handshake.msg_type) {
//         case none:      struct{};
//         case user_add:  UserAdd;
//         case group_add: GroupAdd;
//         case update:    Update;
//         case delete:    Delete;
//     };
//
//     GroupPreKey pre_key;
//
//     uint32 signer_index;
//     MerkleNode identity_proof<1..2^16-1>;
//     SignaturePublicKey identity_key;
//
//     SignatureScheme algorithm;
//     opaque signature<1..2^16-1>;
// } Handshake;
//
// TODO(rlb@ipv.sx): Add credentials
// TODO(rlb@ipv.sx): Crypto agility
type Handshake struct {
	Body          HandshakeMessageBody
	PreKey        GroupPreKey
	SignerIndex   uint32
	IdentityProof MerklePath
	IdentityKey   SignaturePublicKey
	Signature     []byte
}

func (h *Handshake) Sign(identityKey SignaturePrivateKey) error {
	h.IdentityKey = identityKey.PublicKey

	// Marshal, then trim off the header octets for the empty signature
	h.Signature = nil
	tbs, err := syntax.Marshal(h)
	if err != nil {
		return err
	}
	tbs = tbs[:len(tbs)-2]

	h.Signature = identityKey.sign(tbs)
	return err
}

func (h Handshake) IdentityRoot() ([]byte, error) {
	return h.PreKey.IdentityFrontier.RootAsFrontier()
}

func (h Handshake) Verify(identityRoot []byte) error {
	// Verify the signature
	tbs, err := syntax.Marshal(h)
	if err != nil {
		return err
	}
	tbs = tbs[:len(tbs)-len(h.Signature)-2]

	if !h.IdentityKey.verify(tbs, h.Signature) {
		return fmt.Errorf("Invalid signature")
	}

	// Verify roster proof if provided
	if identityRoot != nil {
		index := uint(h.SignerIndex)
		size := uint(h.PreKey.GroupSize)
		leaf := NewMerkleNode(h.IdentityKey)
		root, err := h.IdentityProof.RootAsCopath(index, size, leaf)
		if err != nil {
			return err
		}

		if !bytes.Equal(root, identityRoot) {
			return fmt.Errorf("Merkle inclusion check failed")
		}
	}

	return nil
}

type rawHandshake struct {
	MsgType       HandshakeType
	MsgBody       []byte `tls:"head=3"`
	PreKey        GroupPreKey
	SignerIndex   uint32
	IdentityProof []MerkleNode `tls:"min=0,head=2"`
	IdentityKey   SignaturePublicKey
	Signature     []byte `tls:"min=0,head=2"`
}

func (h Handshake) MarshalTLS() ([]byte, error) {
	body, err := syntax.Marshal(h.Body)
	if err != nil {
		return nil, err
	}

	raw := rawHandshake{
		MsgType:       h.Body.Type(),
		MsgBody:       body,
		PreKey:        h.PreKey,
		SignerIndex:   h.SignerIndex,
		IdentityProof: h.IdentityProof,
		IdentityKey:   h.IdentityKey,
		Signature:     h.Signature,
	}
	return syntax.Marshal(raw)
}

func (h *Handshake) UnmarshalTLS(data []byte) (int, error) {
	var raw rawHandshake
	n, err := syntax.Unmarshal(data, &raw)
	if err != nil {
		return 0, err
	}

	switch raw.MsgType {
	case HandshakeTypeNone:
		h.Body = new(None)
	case HandshakeTypeUserAdd:
		h.Body = new(UserAdd)
	case HandshakeTypeGroupAdd:
		h.Body = new(GroupAdd)
	case HandshakeTypeUpdate:
		h.Body = new(Update)
	case HandshakeTypeDelete:
		h.Body = new(Delete)
	}

	_, err = syntax.Unmarshal(raw.MsgBody, h.Body)
	if err != nil {
		return 0, err
	}

	h.PreKey = raw.PreKey
	h.SignerIndex = raw.SignerIndex
	h.IdentityProof = raw.IdentityProof
	h.IdentityKey = raw.IdentityKey
	h.Signature = raw.Signature

	return n, nil
}
