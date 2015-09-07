package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jteeuwen/audible"
	"golang.org/x/crypto/tea"
)

// http://download.audible.com/webinstall/Audible-Palm-iTunes.dmg has symbols, yay!
// https://play.google.com/store/apps/details?id=com.audible.application is friendly too.
// For debugging, use "Audible Manager" (Audible Software for Windows PC)
//
// 4 -> MP3 (Format 4)
// 5 -> acelp16 (ACELP.net 16, Format 3)
// 6 -> acelp85 (ACELP.net 8.5, Format 2
// Search for "GetSecondSizeByCodecID" in Audible binary blobs ;)
//
// libavcodec/sipr.c from FFmpeg project
// switch(block_aign) {
//     case 20: ctx->mode = MODE_16k; break;
//     case 19: ctx->mode = MODE_8k5; break;
//     case 29: ctx->mode = MODE_6k5; break;
//     case 37: ctx->mode = MODE_5k0; break;
//
// Also see,
//
// http://www.audible.com/audioformats
// http://esec-lab.sogeti.com/static/publications/10-hitbkl-drm.pdf
func GetCodecParamsByCodecName(codec_name string) (int, uint16, uint16, uint32, uint16) {
	codec_second_size := -1
	file_type := -1
	var fmt_type uint16 = 0
	var BlockAlign uint16 = 1
	var SampleRate uint32 = 0
	var NumChannels uint16 = 1 // https://en.wikipedia.org/wiki/Audible.com#Quality

	if codec_name == "mp332" {
		// case 0xC00D
		codec_second_size = 3982
		file_type = 4
	} else if codec_name == "acelp16" {
		// case 0xC00C
		codec_second_size = 2000
		file_type = 5
	} else if codec_name == "acelp85" {
		// case 0xC010
		codec_second_size = 1045
		file_type = 6
	}

	if file_type == 4 {
		fmt_type = 0x55 // Compression Codec == MP3
		BlockAlign = 1
		SampleRate = 22050
	} else if file_type == 5 {
		fmt_type = 0x130 // Compression Codec == ACELP.net
		BlockAlign = 20  // acelp16
		SampleRate = 16000
	} else if file_type == 6 { // acelp85
		fmt_type = 0x130
		BlockAlign = 19
		SampleRate = 8500
	}

	return codec_second_size, fmt_type, BlockAlign, SampleRate, NumChannels
}

// http://www-mmsp.ece.mcgill.ca/documents/AudioFormats/WAVE/WAVE.html
func writeHeader(outf *os.File, of *bufio.Writer, codec_name string) int64 {
	codec_second_size, fmt_type, BlockAlign, SampleRate, NumChannels := GetCodecParamsByCodecName(codec_name)
	if codec_second_size == -1 {
		fmt.Println("bad codec?", codec_name, codec_second_size)
		panic("v_v")
	} else {
		// fmt.Printf("CodecSecondSize for %s is %d\n", codec_name, codec_second_size)
	}
	// RIFF
	tokenRiff := [4]byte{'R', 'I', 'F', 'F'}
	var filesize uint32 = 0

	// WAVE
	tokenWaveFormat := [4]byte{'W', 'A', 'V', 'E'}

	// fmt
	tokenChunkFmt := [4]byte{'f', 'm', 't', ' '}

	type riffChunkFmt struct {
		LengthOfHeader uint32
		AudioFormat    uint16 // 1 = PCM (not compressed)
		NumChannels    uint16
		SampleRate     uint32
		BytesPerSec    uint32
		BytesPerBloc   uint16
		BitsPerSample  uint16
	}

	chunkFmt := riffChunkFmt{
		LengthOfHeader: 16,       // for "fmt" chunk
		AudioFormat:    fmt_type, // 1 for PCM
		NumChannels:    NumChannels,
		SampleRate:     SampleRate,
		BytesPerSec:    (uint32)(codec_second_size),
		BytesPerBloc:   BlockAlign,
		BitsPerSample:  0,
	}

	// fact
	var fixed_value_1 uint32 = 4
	var fixed_value_2 uint32 = 0
	tokenFact := [4]byte{'f', 'a', 'c', 't'}

	tokenData := [4]byte{'d', 'a', 't', 'a'}
	var datasize uint32 = 0 // this is fixed just before the file close!

	of.Write(tokenRiff[:])
	binary.Write(of, binary.BigEndian, filesize)
	of.Write(tokenWaveFormat[:])
	of.Write(tokenChunkFmt[:])
	binary.Write(of, binary.LittleEndian, chunkFmt)
	of.Write(tokenFact[:])
	binary.Write(of, binary.LittleEndian, fixed_value_1)
	binary.Write(of, binary.LittleEndian, fixed_value_2)
	of.Write(tokenData[:])
	of.Flush()
	datasize_offset, _ := outf.Seek(0, os.SEEK_CUR)
	binary.Write(of, binary.BigEndian, datasize)

	return datasize_offset
}

func main() {
	splitPtr := flag.Bool("split", false, "generate split files")
	flag.Parse()
	doSplit := *splitPtr
	args := flag.Args()

	if len(args) < 2 {
		fmt.Printf("Usage: %s input.aa out-prefix\n", os.Args[0])
		os.Exit(-1)
	}

	h, err := audible.ReadFile(args[0])
	if err != nil {
		panic(err)
	}

	// profiling code
	// pf, _ := os.Create("AA-ng.cpuprofile")
	// pprof.StartCPUProfile(pf)
	// defer pprof.StopCPUProfile()

	for tag, value := range h.Tags {
		fmt.Println(tag, value)
	}

	fmt.Println("TOC size is", len(h.TOC))
	// fmt.Println("Header Terminator Block", h.Unknown)

	/* KDF (AADecryptAudibleHeader) */
	fixed_key := []byte{0x77, 0x21, 0x4d, 0x4b,
		0x19, 0x6A, 0x87, 0xCD,
		0x52, 0x00, 0x45, 0xFD,
		0x2A, 0x51, 0xD6, 0x73}
	c, _ := tea.NewCipherWithRounds(fixed_key, 16)
	dst := make([]byte, 8)
	buf := make([]byte, 8)
	mashed_data := new(bytes.Buffer) // 132 bytes in original soure code
	var padding uint16 = 0
	_ = binary.Write(mashed_data, binary.BigEndian, padding)     // 2 bytes (purely for padding purposes)
	_ = binary.Write(mashed_data, binary.BigEndian, h.HeaderKey) // 16 bytes
	output := mashed_data.Bytes()
	// fmt.Println("Mashed Data >", hex.EncodeToString(output))
	size := len(mashed_data.Bytes())
	rounded_size := 8*(size/8) + 8
	idx := 0
	v0 := h.HeaderSeed
	v1 := h.HeaderSeed + 1
	for i := 0; i < rounded_size; i += 8 { // for every block
		// prepare input
		v0 = h.HeaderSeed
		v1 = h.HeaderSeed + 1
		src := new(bytes.Buffer)
		_ = binary.Write(src, binary.BigEndian, v0)
		_ = binary.Write(src, binary.BigEndian, v1)

		h.HeaderSeed = v1 + 1
		c.Encrypt(dst, src.Bytes()) // TEA encrypt

		for j := 0; j < 8 && idx < size; j, idx = j+1, idx+1 {
			output[idx] = output[idx] ^ dst[j]
		}
	}
	// fmt.Println("Final Output >", hex.EncodeToString(output))
	// skip first 2 bytes of output (h.Secret padding)
	final_key := output[2 : 2+16] // 128-bit TEA key
	fmt.Println("Final Key >", hex.EncodeToString(final_key))

	// find start of audio data, https://github.com/jteeuwen/audible/blob/master/MAKINGOF.md
	largest_idx := 1 // skip the first entry!
	largest_gap := 0
	for idx, v := range h.TOC {
		if idx == 0 {
			continue
		}
		s, e := (int)(v[0]), (int)(v[1])
		current_gap := e - s
		if current_gap > largest_gap {
			largest_idx = idx
			largest_gap = current_gap
		}
	}
	fmt.Println("Full TOC is", h.TOC)
	fmt.Println("Selecting TOC entry", largest_idx, "as audio data marker", h.TOC[largest_idx])
	data := h.TOC[largest_idx]
	start, end := data[0], data[1]
	fmt.Println("Start >", start, "End >", end)
	inf, err := os.Open(args[0])
	if err != nil {
		panic(err)
	}
	inf.Seek((int64)(start), os.SEEK_SET)
	f := bufio.NewReader(inf)

	tbuf := make([]byte, 4)
	e := binary.BigEndian

	codec_name := h.Tags["codec"]

	total_parsed := 0
	total_written := 0
	total_seconds := 0.0
	chapter_idx := 0 // find total number of chapters?

	var outf *os.File
	var m3uf *os.File
	var of *bufio.Writer
	var datasize_offset int64 = 0
	var part_filename string
	codec_second_size, _, _, _, _ := GetCodecParamsByCodecName(codec_name)

	if codec_second_size == -1 {
		fmt.Println("bad codec?", codec_name, codec_second_size)
		panic("v_v")
	} else {
		fmt.Printf("CodecSecondSize for %s is %d\n", codec_name, codec_second_size)
	}

	if !doSplit {
		outf, err = os.Create(args[1])
		of = bufio.NewWriter(outf)
		if err != nil {
			panic(err)
		}
		datasize_offset = writeHeader(outf, of, codec_name)
	} else {
		m3uf, _ = os.Create(fmt.Sprintf("%s.m3u", args[1]))
		fmt.Fprintf(m3uf, "#EXTM3U\n")
	}

	// chapter_size -> codec_second_size -> TEA blocks + trailing_bytes
	for {
		if doSplit {
			part_filename = fmt.Sprintf("%s-chapter-%d.wav", args[1], chapter_idx)
			outf, _ = os.Create(part_filename)
			of = bufio.NewWriter(outf)
			if err != nil {
				panic(err)
			}
			total_written = 0
			datasize_offset = writeHeader(outf, of, codec_name)
		}

		io.ReadFull(f, tbuf)
		chapter_size := (int)(e.Uint32(tbuf))
		io.ReadFull(f, tbuf)
		data_start_offset := (int)(e.Uint32(tbuf))

		// is this the "chapter marker" block?
		seconds := (float64)(chapter_size / codec_second_size)
		total_seconds = total_seconds + seconds
		// out_minutes := (int)(total_seconds) / 60
		// out_seconds := (int)(total_seconds) % 60
		// out_hours := out_minutes / 60
		// out_minutes = out_minutes % 60
		// fmt.Printf("Chapter %d -> %d:%02d:%02d\n", chapter_idx, out_hours, out_minutes, out_seconds)

		// "Audible Manager" has a custom implementation for handling
		// chapters with perfect precision.  It knows the exact data
		// offsets for the chapters (we have this information too, but
		// converting these data offsets to timestamps is a big problem
		// (which doesn't seem to have a solution). Try imagining how
		// a .wav reader (VLC / mpv) will try to convert the timestamps
		// to seek offsets (good problem, right?).
		//
		// If we skip converting data offsets (of the chapters) into
		// timestamps, and instead generate split files, we avoid the
		// whole problem entirely.
		if doSplit {
			fmt.Fprintf(m3uf, "\n#EXTINF:%d,%s - Chapter %d\n", (int)(seconds), h.Tags["title"], chapter_idx)
			fmt.Fprintf(m3uf, "%s\n", part_filename)
		}
		chapter_idx = chapter_idx + 1

		fmt.Printf("Chapter Header Block > Data Size %d bytes (%0.1f seconds %0.1f total) Data Start %d (Out Written %d) (Chapter %d)\n", chapter_size, seconds, total_seconds, data_start_offset, total_written, chapter_idx)

		total_parsed = total_parsed + 8 // not written but let's track these 8 bytes!

		// process "chapter_size" bytes!
		current_codec_second_size := codec_second_size
		total_codec_sized_blocks := (chapter_size-1)/current_codec_second_size + 1

		for current_codec_sized_block := 0; current_codec_sized_block < total_codec_sized_blocks; current_codec_sized_block++ {
			// do we have trailing bytes in the last block?
			if current_codec_sized_block == total_codec_sized_blocks-1 && chapter_size%current_codec_second_size != 0 {
				current_codec_second_size = chapter_size % codec_second_size
				// fmt.Println("Codec size changed to", current_codec_second_size)
			}

			// decrypt data using TEA
			blocks := current_codec_second_size / 8 // TEA blocks
			for i := 0; i < blocks; i++ {
				_, _ = io.ReadFull(f, buf)
				c, _ = tea.NewCipherWithRounds(final_key, 16)
				c.Decrypt(dst, buf)
				of.Write(dst)
			}
			total_parsed = total_parsed + 8*blocks
			total_written = total_written + 8*blocks

			// trailing bytes are left unencrypted!
			trailing_bytes := current_codec_second_size % 8
			if trailing_bytes != 0 {
				tmp := make([]byte, trailing_bytes)
				_, _ = io.ReadFull(f, tmp)
				of.Write(tmp)
				total_parsed = total_parsed + trailing_bytes
				total_written = total_written + trailing_bytes
			}
		}
		// end of this chapter
		if doSplit {
			of.Flush()
			/* fix the .wav headers */
			outf.Seek(datasize_offset, os.SEEK_SET)
			binary.Write(outf, binary.LittleEndian, (uint32)(total_written))
			outf.Close()
		}

		if total_parsed >= (int)(end) {
			break
		}
	}
	// end of all chapters
	if !doSplit {
		of.Flush()
		outf.Seek(datasize_offset, os.SEEK_SET)
		binary.Write(outf, binary.LittleEndian, (uint32)(total_written))
		outf.Close()
	}

	m3uf.Close()
	inf.Close()
}
