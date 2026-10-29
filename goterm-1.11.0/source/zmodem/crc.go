package zmodem

import "hash/crc32"

var crc16Table [256]uint16

func init() {
	for i := 0; i < 256; i++ {
		crc := uint16(i) << 8
		for b := 0; b < 8; b++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
		crc16Table[i] = crc
	}
}

// crc16Update is the plain (non-augmented) CRC-16/XMODEM step. ZMODEM's
// "UPDC16 with two trailing zero bytes" formulation yields the same value.
func crc16Update(crc uint16, b byte) uint16 {
	return crc<<8 ^ crc16Table[byte(crc>>8)^b]
}

func crc16(data ...[]byte) uint16 {
	var c uint16
	for _, d := range data {
		for _, b := range d {
			c = crc16Update(c, b)
		}
	}
	return c
}

// crc32 is the standard IEEE CRC-32 (init/final 0xFFFFFFFF), sent LSB first.
func crc32sum(data ...[]byte) uint32 {
	var c uint32
	for _, d := range data {
		c = crc32.Update(c, crc32.IEEETable, d)
	}
	return c
}
