package trust

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

func validateNativeMagic(f *os.File, goos string) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hdr := make([]byte, 4)
	n, err := io.ReadFull(f, hdr)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}
	if n < 2 {
		return ReasonNotExecutableType
	}
	switch goos {
	case "windows":
		if hdr[0] != 'M' || hdr[1] != 'Z' {
			return fmt.Errorf("%w: pe", ReasonNotExecutableType)
		}
	case "linux":
		if n < 4 || hdr[0] != 0x7f || hdr[1] != 'E' || hdr[2] != 'L' || hdr[3] != 'F' {
			return fmt.Errorf("%w: elf", ReasonNotExecutableType)
		}
	case "darwin":
		if n < 4 {
			return fmt.Errorf("%w: macho", ReasonNotExecutableType)
		}
		magic := binary.LittleEndian.Uint32(hdr)
		switch magic {
		case 0xfeedface, 0xcefaedfe, 0xfeedfacf, 0xcffaedfe, 0xcafebabe, 0xbebafeca:
		default:
			return fmt.Errorf("%w: macho", ReasonNotExecutableType)
		}
	default:
		return ReasonStagingUnsupported
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return nil
}
