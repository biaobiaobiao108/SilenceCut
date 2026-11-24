//go:build ignore

package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: make-icon source.svg target.ico|target.icns")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	icon := ui.MustParseSVG(data)
	raster := func(size int) []byte {
		var image bytes.Buffer
		if err := png.Encode(&image, icon.Image(size, size, nil)); err != nil {
			log.Fatal(err)
		}
		return image.Bytes()
	}
	var result bytes.Buffer
	switch filepath.Ext(os.Args[2]) {
	case ".ico":
		sizes := []int{16, 20, 24, 32, 40, 48, 64, 128, 256}
		binary.Write(&result, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
		offset := uint32(6 + 16*len(sizes))
		var images bytes.Buffer
		for _, size := range sizes {
			data := raster(size)
			// An ICO dimension of 0 represents 256 pixels.
			result.Write([]byte{byte(size), byte(size), 0, 0})
			binary.Write(&result, binary.LittleEndian, [2]uint16{1, 32})
			binary.Write(&result, binary.LittleEndian, [2]uint32{uint32(len(data)), offset})
			offset += uint32(len(data))
			images.Write(data)
		}
		result.Write(images.Bytes())
	case ".icns":
		var images bytes.Buffer
		types := []string{"icp4", "icp5", "icp6", "ic07", "ic08", "ic09", "ic10"}
		for i, kind := range types {
			data := raster(16 << i)
			images.WriteString(kind)
			binary.Write(&images, binary.BigEndian, uint32(8+len(data)))
			images.Write(data)
		}
		result.WriteString("icns")
		binary.Write(&result, binary.BigEndian, uint32(8+images.Len()))
		result.Write(images.Bytes())
	default:
		log.Fatal("target must end in .ico or .icns")
	}
	if err := os.WriteFile(os.Args[2], result.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}
