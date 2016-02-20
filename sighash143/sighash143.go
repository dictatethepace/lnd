package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"strings"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/uspv"
)

const (
	inspk0 = "2103c9f4836b9a4f77fc0d81f7bcb01b7f1b35916864b9476c241ce9fc198bd25432ac"
	inamt0 = int64(625000000)

	inspk1 = "00141d0f172a0ecb48aee1be1f2687d2963ae33f71a1"
	inamt1 = int64(600000000)
)

// calcWitnessSignatureHash is the witnessified version of calcSignatureHash
func calcWitnessSignatureHash(
	hashType txscript.SigHashType, tx *wire.MsgTx, idx int, amt int64) []byte {
	// in the script.go calcSignatureHash(), idx is assumed safe, so I guess
	// that's OK here too...

	// first get hashPrevOuts, hashSequence, and hashOutputs
	hashPrevOuts := calcHashPrevOuts(tx, hashType)
	hashSequence := calcHashSequence(tx, hashType)
	hashOutputs := calcHashOutputs(tx, idx, hashType)

	var buf4 [4]byte // buffer for 4-byte stuff
	var buf8 [8]byte // buffer for 8-byte stuff
	var pre []byte   // the pre-image we're generating

	binary.LittleEndian.PutUint32(buf4[:], uint32(tx.Version))
	pre = append(pre, buf4[:]...)

	pre = append(pre, hashPrevOuts.Bytes()...)
	pre = append(pre, hashSequence.Bytes()...)

	// outpoint being spent
	pre = append(pre, tx.TxIn[idx].PreviousOutPoint.Hash.Bytes()...)
	binary.LittleEndian.PutUint32(buf4[:], tx.TxIn[idx].PreviousOutPoint.Index)
	pre = append(pre, buf4[:]...)

	// scriptCode which is some new thing
	sCode := []byte{0x19, 0x76, 0xa9, 0x14}
	sCode = append(sCode, tx.TxIn[idx].SignatureScript[2:22]...)
	sCode = append(sCode, []byte{0x88, 0xac}...)
	pre = append(pre, sCode...)

	// amount being signed off
	binary.LittleEndian.PutUint64(buf8[:], uint64(amt))
	pre = append(pre, buf8[:]...)

	// nsequence of input
	binary.LittleEndian.PutUint32(buf4[:], tx.TxIn[idx].Sequence)
	pre = append(pre, buf4[:]...)

	pre = append(pre, hashOutputs.Bytes()...)

	// locktime
	binary.LittleEndian.PutUint32(buf4[:], tx.LockTime)
	pre = append(pre, buf4[:]...)

	// hashType... in 4 bytes, instead of 1, because reasons.
	binary.LittleEndian.PutUint32(buf4[:], uint32(hashType))
	pre = append(pre, buf4[:]...)

	fmt.Printf("pre: %x\n", pre)
	hsh := wire.DoubleSha256SH(pre)
	return hsh.Bytes()
}

// calcHashPrevOuts makes a single hash of all previous outputs in the tx
func calcHashPrevOuts(tx *wire.MsgTx, hType txscript.SigHashType) wire.ShaHash {
	// skip this (0x00) for anyonecanpay
	if hType == txscript.SigHashAnyOneCanPay {
		var empty [32]byte
		return empty
	}

	var pre []byte
	for _, in := range tx.TxIn {
		// first append 32 byte hash
		pre = append(pre, in.PreviousOutPoint.Hash.Bytes()...)
		// then make a buffer, put 4 byte index in lil' endian and append that
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], in.PreviousOutPoint.Index)
		pre = append(pre, buf[:]...)
	}
	fmt.Printf("pre: %x\n", pre)
	return wire.DoubleSha256SH(pre)
}

// calcHashSequence is hash of txins' seq numbers, lil' endian, stuck together
func calcHashSequence(tx *wire.MsgTx, hType txscript.SigHashType) wire.ShaHash {
	// skip (0x00) for single, none, anyonecanpay
	if hType == txscript.SigHashSingle || hType == txscript.SigHashNone ||
		hType == txscript.SigHashAnyOneCanPay {
		var empty [32]byte
		return empty
	}

	var pre []byte
	for _, in := range tx.TxIn {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], in.Sequence)
		pre = append(pre, buf[:]...)
	}
	fmt.Printf("pre: %x\n", pre)
	return wire.DoubleSha256SH(pre)
}

// calcHashOutputs also wants a input index, which it only uses for
// sighash single.  If it's not sighash single, just put a 0 or whatever.
func calcHashOutputs(
	tx *wire.MsgTx, inIndex int, hType txscript.SigHashType) wire.ShaHash {
	if hType == txscript.SigHashNone ||
		(hType == txscript.SigHashSingle && inIndex <= len(tx.TxOut)) {
		var empty [32]byte
		return empty
	}
	if hType == txscript.SigHashSingle {
		var buf bytes.Buffer
		writeTxOut(&buf, 0, 0, tx.TxOut[inIndex])
		return wire.DoubleSha256SH(buf.Bytes())
	}

	var pre []byte
	for _, out := range tx.TxOut {
		var buf bytes.Buffer
		writeTxOut(&buf, 0, 0, out)
		pre = append(pre, buf.Bytes()...)
	}
	fmt.Printf("pre: %x\n", pre)
	return wire.DoubleSha256SH(pre)
}

func main() {
	fmt.Printf("sighash 143\n")

	// get previous pkscripts for inputs 0 and 1 from hex
	in0spk, err := hex.DecodeString(string(inspk0))
	if err != nil {
		log.Fatal(err)
	}
	in1spk, err := hex.DecodeString(string(inspk1))
	if err != nil {
		log.Fatal(err)
	}

	// load tx skeleton from local file
	fmt.Printf("loading tx from file tx.hex\n")
	txhex, err := ioutil.ReadFile("tx.hex")
	if err != nil {
		log.Fatal(err)
	}

	txhex = []byte(strings.TrimSpace(string(txhex)))
	txbytes, err := hex.DecodeString(string(txhex))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("loaded %d byte tx %x\n", len(txbytes), txbytes)

	// 	make tx
	ttx := wire.NewMsgTx()

	// deserialize into tx
	buf := bytes.NewBuffer(txbytes)
	ttx.Deserialize(buf)

	ttx.TxIn[0].SignatureScript = in0spk
	ttx.TxIn[1].SignatureScript = in1spk

	fmt.Printf(uspv.TxToString(ttx))

	hxh := calcWitnessSignatureHash(txscript.SigHashAll, ttx, 1, inamt1)

	fmt.Printf("got sigHash %x\n", hxh)
}

// pver can be 0, doesn't do anything in these.  Same for msg.Version
// writeVarInt serializes val to w using a variable number of bytes depending
// on its value.
func writeVarInt(w io.Writer, pver uint32, val uint64) error {
	if val < 0xfd {
		_, err := w.Write([]byte{uint8(val)})
		return err
	}

	if val <= math.MaxUint16 {
		var buf [3]byte
		buf[0] = 0xfd
		binary.LittleEndian.PutUint16(buf[1:], uint16(val))
		_, err := w.Write(buf[:])
		return err
	}

	if val <= math.MaxUint32 {
		var buf [5]byte
		buf[0] = 0xfe
		binary.LittleEndian.PutUint32(buf[1:], uint32(val))
		_, err := w.Write(buf[:])
		return err
	}

	var buf [9]byte
	buf[0] = 0xff
	binary.LittleEndian.PutUint64(buf[1:], val)
	_, err := w.Write(buf[:])
	return err
}

// writeVarBytes serializes a variable length byte array to w as a varInt
// containing the number of bytes, followed by the bytes themselves.
func writeVarBytes(w io.Writer, pver uint32, bytes []byte) error {
	slen := uint64(len(bytes))
	err := writeVarInt(w, pver, slen)
	if err != nil {
		return err
	}

	_, err = w.Write(bytes)
	if err != nil {
		return err
	}
	return nil
}

// writeTxOut encodes to into the bitcoin protocol encoding for a transaction
// output (TxOut) to w.
func writeTxOut(w io.Writer, pver uint32, version int32, to *wire.TxOut) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(to.Value))
	_, err := w.Write(buf[:])
	if err != nil {
		return err
	}

	err = writeVarBytes(w, pver, to.PkScript)
	if err != nil {
		return err
	}
	return nil
}
