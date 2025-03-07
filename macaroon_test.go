package macaroon

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/stretchr/testify/require"
	msgpack "github.com/vmihailenco/msgpack/v5"
)

func cavExpiry(d time.Duration) Caveat {
	return &ValidityWindow{
		NotBefore: time.Now().Unix(),
		NotAfter:  time.Now().Add(d).Unix(),
	}
}

const (
	cavTestParentResource = iota + CavMinUserDefined
	cavTestChildResource
)

type testCaveatParentResource struct {
	ID         uint64
	Permission Action
}

func init() { RegisterCaveatType("ParentResource", cavTestParentResource, &testCaveatParentResource{}) }

func cavParent(permission Action, id uint64) Caveat {
	return &testCaveatParentResource{id, permission}
}

func (c *testCaveatParentResource) CaveatType() CaveatType {
	return cavTestParentResource
}

func (c *testCaveatParentResource) Prohibits(f Access) error {
	tf, isTestAccess := f.(*testAccess)

	switch {
	case !isTestAccess:
		return ErrInvalidAccess
	case tf.parentResource == nil:
		return ErrResourceUnspecified
	case *tf.parentResource != c.ID:
		return fmt.Errorf("%w resource", ErrUnauthorizedForResource)
	case !tf.action.IsSubsetOf(c.Permission):
		return fmt.Errorf("%w action", ErrUnauthorizedForAction)
	default:
		return nil
	}
}

func (c *testCaveatParentResource) IsAttestation() bool {
	return false
}

type testCaveatChildResource struct {
	ID         uint64
	Permission Action
}

func init() { RegisterCaveatType("ChildResource", cavTestChildResource, &testCaveatChildResource{}) }

func cavChild(permission Action, id uint64) Caveat {
	return &testCaveatChildResource{id, permission}
}

func (c *testCaveatChildResource) CaveatType() CaveatType {
	return cavTestChildResource
}

func (c *testCaveatChildResource) Prohibits(f Access) error {
	tf, isTestAccess := f.(*testAccess)

	switch {
	case !isTestAccess:
		return ErrInvalidAccess
	case tf.childResource == nil:
		return ErrResourceUnspecified
	case *tf.childResource != c.ID:
		return fmt.Errorf("%w resource", ErrUnauthorizedForResource)
	case !tf.action.IsSubsetOf(c.Permission):
		return fmt.Errorf("%w action", ErrUnauthorizedForAction)
	default:
		return nil
	}
}

func (c *testCaveatChildResource) IsAttestation() bool {
	return false
}

type testAccess struct {
	action         Action
	parentResource *uint64
	childResource  *uint64
	now            time.Time
}

var _ Access = (*testAccess)(nil)

func (f *testAccess) GetAction() Action { return f.action }

func (f *testAccess) Now() time.Time {
	if f.now.IsZero() {
		return time.Now()
	}
	return f.now
}

func (f *testAccess) Validate() error {
	if f.childResource != nil && f.parentResource == nil {
		return ErrResourceUnspecified
	}
	return nil
}

func TestMacaroons(t *testing.T) {
	type tpParams struct {
		key  EncryptionKey
		loc  string
		cavs []Caveat
	}

	var (
		kid, encoded []byte
		discharges   [][]byte
		loc          string
		key          SigningKey
		mac          *Macaroon
		decoded      *Macaroon
		decodedCavs  []Caveat
		cavs         []Caveat
		verifiedCavs *CaveatSet
		tpCavs       []tpParams
		err          error
	)

	reset := func(t *testing.T) {
		t.Helper()

		kid = []byte("kid")
		loc = "loc"
		key = NewSigningKey()
		cavs = nil
		tpCavs = nil
		mac = nil
		encoded = nil
		discharges = nil
		decoded = nil
		decodedCavs = nil
		verifiedCavs = nil

	}
	reset(t)

	requireMint := func(t *testing.T) {
		t.Helper()

		mac, err = New(kid, loc, key)
		require.NoError(t, err)

		mac.Add(cavs...)

		for _, tp := range tpCavs {
			mac.Add3P(tp.key, tp.loc, tp.cavs...)
		}

		encoded, err = mac.Encode()
		require.NoError(t, err)

		for _, tp := range tpCavs {
			found, _, dm, err := dischargeMacaroon(tp.key, tp.loc, encoded)
			assert.True(t, found)
			require.NoError(t, err)

			dmBuf, err := dm.Encode()
			require.NoError(t, err)

			discharges = append(discharges, dmBuf)
		}
	}

	requireDecode := func(t *testing.T) {
		t.Helper()
		if encoded == nil {
			requireMint(t)
		}

		decoded, err = Decode(encoded)
		require.NoError(t, err)

		decodedCavs = decoded.UnsafeCaveats.Caveats
	}

	requireVerify := func(t *testing.T) {
		t.Helper()
		if decoded == nil {
			requireDecode(t)
		}

		verifiedCavs, err = decoded.Verify(key, discharges, nil)
		require.NoError(t, err)
	}

	t.Run("decode", func(t *testing.T) {
		defer reset(t)
		requireDecode(t)

		assert.Equal(t, loc, decoded.Location)
	})

	t.Run("decode MacaroonNonce", func(t *testing.T) {
		defer reset(t)
		requireDecode(t)

		assert.Equal(t, kid, decoded.Nonce.KID)
		assert.Equal(t, nonceRndSize, len(decoded.Nonce.Rnd))
	})

	t.Run("decode Caveat", func(t *testing.T) {
		defer reset(t)
		cavs = append(cavs, cavParent(ActionRead, 123))
		requireDecode(t)

		assert.Equal(t, 1, len(decodedCavs))
		assert.Equal(t, cavTestParentResource, decodedCavs[0].CaveatType())
		assert.Equal(t, cavs[0], decodedCavs[0])
	})

	t.Run("verify - good signature", func(t *testing.T) {
		defer reset(t)
		requireVerify(t)

		assert.Equal(t, mac.UnsafeCaveats, *verifiedCavs)
	})

	t.Run("verify - with 1p caveat", func(t *testing.T) {
		defer reset(t)
		cavs = append(cavs, cavParent(ActionWrite, 234))
		requireVerify(t)
	})

	t.Run("verify - with 3p caveat", func(t *testing.T) {
		defer reset(t)
		tpCavs = append(tpCavs, tpParams{
			key:  NewEncryptionKey(),
			loc:  "other loc",
			cavs: nil,
		})
		requireVerify(t)
	})

	t.Run("verify - with undischarged 3p caveat", func(t *testing.T) {
		defer reset(t)
		tpCavs = append(tpCavs, tpParams{
			key:  NewEncryptionKey(),
			loc:  "other loc",
			cavs: nil,
		})
		requireDecode(t)

		_, err := decoded.Verify(key, nil, nil)
		assert.Error(t, err)
	})

	t.Run("verify - bad signature", func(t *testing.T) {
		defer reset(t)
		requireDecode(t)
		decoded.Tail = bytes.Repeat([]byte{0xff}, len(decoded.Tail))

		_, err := decoded.Verify(key, nil, nil)
		assert.Error(t, err)
	})

	t.Run("verify - bad key", func(t *testing.T) {
		defer reset(t)
		requireDecode(t)
		key = bytes.Repeat([]byte{0xff}, len(key))

		_, err := decoded.Verify(key, nil, nil)
		assert.Error(t, err)
	})

	t.Run("verify - plain discharge signature", func(t *testing.T) {
		defer reset(t)
		const tpLoc = "other loc"
		tpKey := NewEncryptionKey()
		tpCavs = append(tpCavs, tpParams{
			key:  tpKey,
			loc:  tpLoc,
			cavs: nil,
		})
		requireMint(t)

		found, _, dm, err := dischargeMacaroon(tpKey, tpLoc, encoded)
		assert.True(t, found)
		require.NoError(t, err)

		discharges[0], err = dm.Encode()
		require.NoError(t, err)

		requireVerify(t)
	})

	t.Run("verify - bound root token", func(t *testing.T) {
		defer reset(t)
		cav := &BindToParentToken{0xde, 0xad}
		cavs = append(cavs, cav)
		requireDecode(t)

		var tokenBindingIds [][]byte
		_, err := decoded.verify(key, nil, tokenBindingIds, true, nil)
		assert.Error(t, err)

		tokenBindingIds = [][]byte{{0xff}}
		_, err = decoded.verify(key, nil, tokenBindingIds, true, nil)
		assert.Error(t, err)

		tokenBindingIds = [][]byte{{0xde}}
		_, err = decoded.verify(key, nil, tokenBindingIds, true, nil)
		assert.Error(t, err)

		tokenBindingIds = [][]byte{{0xde, 0xad}}
		_, err = decoded.verify(key, nil, tokenBindingIds, true, nil)
		assert.NoError(t, err)

		tokenBindingIds = [][]byte{{0xde, 0xad, 0xbe, 0xef}}
		_, err = decoded.verify(key, nil, tokenBindingIds, true, nil)
		assert.NoError(t, err)
	})

	t.Run("verify - unbound discharge token", func(t *testing.T) {
		defer reset(t)
		tpKey := NewEncryptionKey()
		tpCavs = append(tpCavs, tpParams{
			key:  tpKey,
			loc:  "other loc",
			cavs: nil,
		})
		requireDecode(t)

		unboundDischarge := discharges[0]

		cids, err := decoded.ThirdPartyCIDs()
		require.NoError(t, err)

		cid := cids["other loc"]

		rcid, err := unseal(tpKey, cid)
		require.NoError(t, err)

		wcid := &wireCID{}
		require.NoError(t, msgpack.Unmarshal(rcid, wcid))

		dum, err := Decode(unboundDischarge)
		require.NoError(t, err)

		_, err = dum.verify(wcid.RN, nil, nil, true, nil)
		require.NoError(t, err)

		_, err = dum.verify(wcid.RN, nil, [][]byte{{123}}, true, nil)
		require.NoError(t, err)
	})

	t.Run("verify - bound discharge token", func(t *testing.T) {
		defer reset(t)
		tpKey := NewEncryptionKey()
		const tpLoc = "other loc"
		tpCavs = append(tpCavs, tpParams{
			key:  tpKey,
			loc:  tpLoc,
			cavs: nil,
		})
		requireMint(t)

		found, _, dm, err := dischargeMacaroon(tpKey, tpLoc, encoded)
		assert.True(t, found)
		require.NoError(t, err)

		require.NoError(t, dm.Bind(encoded))

		discharges[0], err = dm.Encode()
		require.NoError(t, err)

		requireVerify(t)
	})

	t.Run("verify - wrongly bound discharge token", func(t *testing.T) {
		defer reset(t)
		tpKey := NewEncryptionKey()
		const tpLoc = "other loc"
		tpCavs = append(tpCavs, tpParams{
			key:  tpKey,
			loc:  tpLoc,
			cavs: nil,
		})
		requireMint(t)

		found, _, dm, err := dischargeMacaroon(tpKey, tpLoc, encoded)
		assert.True(t, found)
		require.NoError(t, err)

		require.NoError(t, dm.Bind(encoded))
		dm.Add(&BindToParentToken{0xde, 0xad, 0xbe, 0xef})

		discharges[0], err = dm.Encode()
		require.NoError(t, err)

		requireDecode(t)

		_, err = decoded.Verify(key, discharges, nil)
		require.Error(t, err)
	})
}

func Test3pe2e(t *testing.T) {
	// test with both proof (new) and not-proof (old) discharge macaroons
	for _, isProof := range []bool{true, false} {
		t.Run(fmt.Sprintf("isProof-%t", isProof), func(t *testing.T) {
			var (
				kid     = rbuf(10)
				key     = NewSigningKey()
				ka      = NewEncryptionKey()
				authLoc = "https://auth.fly.io"
			)

			m, err := New(kid, "https://api.fly.io", key)
			require.NoError(t, err)

			require.NoError(t, m.Add(cavParent(ActionRead|ActionWrite, 110)))
			require.NoError(t, m.Add3P(ka, authLoc))
			rBuf, err := m.Encode()
			require.NoError(t, err)

			rm, err := Decode(rBuf)
			require.NoError(t, err)

			tps, err := rm.ThirdPartyCIDs()
			require.NoError(t, err)

			cid := tps[authLoc]
			_, dm, err := dischargeCID(ka, authLoc, cid, isProof)
			require.NoError(t, err)

			require.NoError(t, dm.Add(cavExpiry(5*time.Minute)))
			aBuf, err := dm.Encode()
			require.NoError(t, err)

			verifiedCavs, err := rm.Verify(key, [][]byte{aBuf}, nil)
			require.NoError(t, err)

			_, _, err = dischargeCID(ka, authLoc, cid, isProof)
			assert.NoError(t, err)
			cid[10] = 0
			_, _, err = dischargeCID(ka, authLoc, cid, isProof)
			assert.Error(t, err)

			err = verifiedCavs.Validate(&testAccess{
				parentResource: ptr(uint64(110)),
				action:         ActionRead | ActionWrite,
			})
			assert.NoError(t, err)
		})
	}
}

func TestAttenuate(t *testing.T) {
	var (
		nonce = sealNonce()
		key   = NewSigningKey()
	)

	m, err := New(nonce, "https://api.fly.io", key)
	assert.NoError(t, err)
	m.Add(cavParent(ActionRead|ActionWrite, 1))
	buf, err := m.Encode()
	assert.NoError(t, err)

	// attenuation is decode, add, encode

	decoded, err := Decode(buf)
	assert.NoError(t, err)
	err = decoded.Add(cavChild(ActionRead, 100))
	assert.NoError(t, err)
	buf, err = decoded.Encode()
	assert.NoError(t, err)

	decoded, err = Decode(buf)
	assert.NoError(t, err)

	m2, err := decoded.Verify(key, nil, nil)
	assert.NoError(t, err)

	t.Logf("%s", m2)
}

func TestSimple3P(t *testing.T) {
	// test with both proof (new) and not-proof (old) discharge macaroons
	for _, isProof := range []bool{true, false} {
		t.Run(fmt.Sprintf("isProof-%t", isProof), func(t *testing.T) {
			var (
				kid     = rbuf(10)
				rootKey = NewSigningKey()
				ka      = NewEncryptionKey()
				rootLoc = "http://api"
				authLoc = "http://auth"
			)

			m, err := New(kid, rootLoc, rootKey)
			require.NoError(t, err)

			require.NoError(t, m.Add(cavParent(ActionRead|ActionWrite, 1010)))
			require.NoError(t, m.Add3P(ka, authLoc))
			rBuf, err := m.Encode()
			require.NoError(t, err)

			decoded, err := Decode(rBuf)
			require.NoError(t, err)

			tps, err := decoded.ThirdPartyCIDs()
			require.NoError(t, err)
			cid := tps[authLoc]

			_, dm, err := dischargeCID(ka, authLoc, cid, isProof)
			require.NoError(t, err)
			require.NoError(t, dm.Add(cavExpiry(5*time.Minute)))
			aBuf, err := dm.Encode()
			require.NoError(t, err)

			_, err = Decode(aBuf)
			require.NoError(t, err)

			verifiedCavs, err := decoded.Verify(rootKey, [][]byte{aBuf}, nil)
			require.NoError(t, err)

			err = verifiedCavs.Validate(&testAccess{
				parentResource: ptr(uint64(1010)),
				action:         ActionRead | ActionWrite,
			})
			require.NoError(t, err)
		})
	}
}

func fuzz(in []byte) []byte {
	out := make([]byte, len(in))
	copy(out, in)

	for i := 0; i < 10; i++ {
		off := rand.Intn(len(out))
		out[off] ^= byte(rand.Intn(255) + 1)
	}

	return out
}

func TestBrokenTokens(t *testing.T) {
	var (
		kid     = rbuf(10)
		rootKey = NewSigningKey()
		ka      = NewEncryptionKey()
		rootLoc = "http://api"
		authLoc = "http://auth"
	)

	m, _ := New(kid, rootLoc, rootKey)
	m.Add(cavParent(ActionRead|ActionWrite, 1010))
	m.Add3P(ka, authLoc)
	rBuf, err := m.Encode()
	assert.NoError(t, err)

	found, _, dm, err := dischargeMacaroon(ka, authLoc, rBuf)
	assert.True(t, found)
	assert.NoError(t, err)
	dm.Add(cavExpiry(5 * time.Minute))
	aBuf, _ := dm.Encode()

	decoded, err := Decode(rBuf)
	assert.NoError(t, err)
	_, err = decoded.Verify(rootKey, [][]byte{aBuf}, nil)
	assert.NoError(t, err)

	_, err = decoded.Verify(rootKey, nil, nil)
	assert.Error(t, err)

	for i := 0; i < 100; i++ {
		frBuf := fuzz(rBuf)
		rm, err := Decode(frBuf)
		if err != nil {
			i -= 1
			continue
		}
		_, err = rm.Verify(rootKey, [][]byte{aBuf}, nil)
		assert.Error(t, err)
	}

	for i := 0; i < 100; i++ {
		faBuf := fuzz(aBuf)
		_, err = decoded.Verify(rootKey, [][]byte{faBuf}, nil)
		assert.Error(t, err)
	}
}

func TestDuplicateCaveats(t *testing.T) {
	var (
		kid     = rbuf(10)
		rootKey = NewSigningKey()
		rootLoc = "http://api"
	)

	m, err := New(kid, rootLoc, rootKey)
	assert.NoError(t, err)

	assert.NoError(t, m.Add(cavParent(ActionAll, 123)))
	assert.Equal(t, 1, len(m.UnsafeCaveats.Caveats))

	assert.NoError(t, m.Add(cavParent(ActionAll, 123)))
	assert.Equal(t, 1, len(m.UnsafeCaveats.Caveats))

	assert.NoError(t, m.Add(cavParent(ActionAll, 123)))
	assert.Equal(t, 1, len(m.UnsafeCaveats.Caveats))

	assert.NoError(t, m.Add(cavParent(ActionAll, 234)))
	assert.Equal(t, 2, len(m.UnsafeCaveats.Caveats))

	assert.NoError(t, m.Add(cavParent(ActionRead, 123)))
	assert.Equal(t, 3, len(m.UnsafeCaveats.Caveats))

	assert.NoError(t, m.Add(cavParent(ActionRead, 234)))
	assert.Equal(t, 4, len(m.UnsafeCaveats.Caveats))

	assert.NoError(t, m.Add(cavParent(ActionAll, 345), cavParent(ActionAll, 345)))
	assert.Equal(t, 5, len(m.UnsafeCaveats.Caveats))
}

func dischargeMacaroon(ka EncryptionKey, location string, encodedMacaroon []byte) (bool, []Caveat, *Macaroon, error) {
	cid, err := ThirdPartyCID(encodedMacaroon, location)
	if err != nil || len(cid) == 0 {
		return false, nil, nil, err
	}

	dcavs, dm, err := DischargeCID(ka, location, cid)
	return true, dcavs, dm, err
}
