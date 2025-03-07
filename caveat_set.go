package macaroon

import (
	"encoding/json"
	"errors"
	"fmt"

	msgpack "github.com/vmihailenco/msgpack/v5"
)

// CaveatSet is how a set of caveats is serailized/encoded.
type CaveatSet struct {
	Caveats []Caveat
}

var (
	_ msgpack.CustomEncoder = (*CaveatSet)(nil)
	_ msgpack.CustomDecoder = (*CaveatSet)(nil)
	_ msgpack.Marshaler     = (*CaveatSet)(nil)
)

// Create a new CaveatSet comprised of the specified caveats.
func NewCaveatSet(caveats ...Caveat) *CaveatSet {
	return &CaveatSet{append([]Caveat{}, caveats...)}
}

// Decodes a set of serialized caveats.
func DecodeCaveats(buf []byte) (*CaveatSet, error) {
	cavs := new(CaveatSet)

	if err := msgpack.Unmarshal(buf, cavs); err != nil {
		return nil, err
	}

	return cavs, nil
}

// Validates that the caveat set permits the specified accesses.
func (c *CaveatSet) Validate(accesses ...Access) error {
	return Validate(c, accesses...)
}

// Helper for validating concretely-typed accesses.
func Validate[A Access](cs *CaveatSet, accesses ...A) error {
	var merr error
	for _, access := range accesses {
		if ferr := access.Validate(); ferr != nil {
			merr = appendErrs(merr, ferr)
			continue
		}

		merr = appendErrs(merr, cs.validateAccess(access))
	}

	return merr
}

func (c *CaveatSet) validateAccess(access Access) error {
	var merr error
	for _, caveat := range c.Caveats {
		if caveat.IsAttestation() {
			continue
		}

		merr = appendErrs(merr, caveat.Prohibits(access))
	}

	return merr
}

// GetCaveats gets any caveats of type T, including those nested within
// IfPresent caveats.
func GetCaveats[T Caveat](c *CaveatSet) (ret []T) {
	for _, cav := range c.Caveats {
		if typed, ok := cav.(T); ok {
			ret = append(ret, typed)
		}

		if typed, ok := cav.(*IfPresent); ok {
			ret = append(ret, GetCaveats[T](typed.Ifs)...)
		}
	}
	return ret
}

// Implements msgpack.Marshaler
func (c CaveatSet) MarshalMsgpack() ([]byte, error) {
	return encode(c)
}

// Implements msgpack.CustomEncoder
func (c CaveatSet) EncodeMsgpack(enc *msgpack.Encoder) error {
	if err := enc.EncodeArrayLen(len(c.Caveats) * 2); err != nil {
		return err
	}

	for _, cav := range c.Caveats {
		if err := enc.EncodeUint(uint64(cav.CaveatType())); err != nil {
			return err
		}

		if err := enc.Encode(cav); err != nil {
			return err
		}
	}

	return nil
}

// Implements msgpack.CustomDecoder
func (c *CaveatSet) DecodeMsgpack(dec *msgpack.Decoder) error {
	aLen, err := dec.DecodeArrayLen()
	if err != nil {
		return err
	}
	if aLen%2 != 0 {
		return errors.New("bad caveat container")
	}

	nCavs := aLen / 2

	if c.Caveats == nil {
		c.Caveats = make([]Caveat, 0, nCavs)
	}

	for i := 0; i < nCavs; i++ {
		t, err := dec.DecodeUint()
		if err != nil {
			return err
		}

		cav, err := typeToCaveat(CaveatType(t))
		if err != nil {
			return err
		}

		if err := dec.Decode(cav); err != nil {
			return err
		}

		c.Caveats = append(c.Caveats, cav)
	}

	return nil
}

func (c CaveatSet) MarshalJSON() ([]byte, error) {
	var (
		jcavs = make([]jsonCaveat, len(c.Caveats))
		err   error
	)

	for i := range c.Caveats {
		ct := c.Caveats[i].CaveatType()
		cts := caveatTypeToString(ct)
		if cts == "" {
			return nil, fmt.Errorf("unregistered caveat type: %d", ct)
		}

		jcavs[i] = jsonCaveat{
			Type: cts,
		}

		if jcavs[i].Body, err = json.Marshal(c.Caveats[i]); err != nil {
			return nil, err
		}
	}

	return json.Marshal(jcavs)
}

func (c *CaveatSet) UnmarshalJSON(b []byte) error {
	jcavs := []jsonCaveat{}

	if err := json.Unmarshal(b, &jcavs); err != nil {
		return err
	}

	c.Caveats = make([]Caveat, len(jcavs))
	for i := range jcavs {
		t := caveatTypeFromString(jcavs[i].Type)

		if c.Caveats[i], _ = typeToCaveat(t); c.Caveats[i] == nil {
			return fmt.Errorf("bad caveat type: %s", jcavs[i].Type)
		}

		if err := json.Unmarshal(jcavs[i].Body, &c.Caveats[i]); err != nil {
			return err
		}
	}

	return nil
}

type jsonCaveat struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}
