package main

import (
	"encoding/binary"
	"math"
)

// IMA ADPCM（Microsoft WAV `wFormatTag = 0x11`）。
//
// 为什么选它（决策见 docs/decisions.md D-29）：4 bit/样本 ⇒ 比 16bit PCM 小 **4 倍**，
// 且是**波形域**编码 —— 没有 MP3 那种变换域预回声（短促打击声最重要的就是这一点），
// 解码是逐样本的、**样本精确对齐**（我们的脚步/挥刀是按动画帧对齐播的），
// 而且编解码器一共一百多行、不需要任何新依赖。
//
// 块布局（微软那套，能被外部工具读）：
//
//	每块 blockAlign 字节：先 4*声道数 字节的块头（每声道 i16 predictor + u8 index + u8 保留），
//	后面是 nibble（**低半字节先**，立体声按 L,R,L,R… 交替）
//	samplesPerBlock = ((blockAlign - 4*channels) * 2 / channels) + 1
//
// ⚠️ 编码器更新预测器时**必须**用与解码器逐位相同的重建式（`imaReconstruct`），
// 否则两边会慢慢漂移（这是这类编解码器最经典的坑）。
//
// ⚠️ 试过并**否掉**的一招：编码端一阶误差反馈（噪声整形，解码端不用改）。
// 在我们的素材上会把重建值顶到 ±32767 —— 自检连续抓到两例
//（源峰值 23363 → 产出 32767；减半阻尼后 22680 → 31718）。削顶artifact 比
// 那几 dB 的 SNR 更糟，所以**不开**，就用最朴素的量化。

// IMA 的步长索引调整表与步长表（标准值）。
var imaIndexTable = [16]int{-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8}

var imaStepTable = [89]int{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31,
	34, 37, 41, 45, 50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143,
	157, 173, 190, 209, 230, 253, 279, 307, 337, 371, 408, 449, 494, 544, 598, 658,
	724, 796, 876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024,
	3327, 3660, 4026, 4428, 4871, 5358, 5894, 6484, 7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899,
	15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
}

// 块字节数：单声道 256 / 立体声 512（微软默认值）。两者每声道的样本数一样。
func imaBlockAlign(channels int) int {
	if channels >= 2 {
		return 512
	}
	return 256
}

func imaSamplesPerBlock(channels int) int {
	ba := imaBlockAlign(channels)
	return (ba-4*channels)*2/channels + 1
}

// imaReconstruct 一个 nibble ⇒ 新预测器与新索引。**编解码共用**（见文件头）。
func imaReconstruct(predictor, index, nibble int) (int, int) {
	step := imaStepTable[index]
	diff := step >> 3
	if nibble&1 != 0 {
		diff += step >> 2
	}
	if nibble&2 != 0 {
		diff += step >> 1
	}
	if nibble&4 != 0 {
		diff += step
	}
	if nibble&8 != 0 {
		predictor -= diff
	} else {
		predictor += diff
	}
	if predictor > 32767 {
		predictor = 32767
	} else if predictor < -32768 {
		predictor = -32768
	}
	index += imaIndexTable[nibble]
	if index < 0 {
		index = 0
	} else if index > 88 {
		index = 88
	}
	return predictor, index
}

// imaEncodeSample 标准 IMA 量化：先按 step 折出 nibble，再用**同一个**重建式更新状态。
func imaEncodeSample(predictor, index, sample int) (nibble, newPred, newIndex int) {
	step := imaStepTable[index]
	delta := sample - predictor
	if delta < 0 {
		nibble = 8
		delta = -delta
	}
	if delta >= step {
		nibble |= 4
		delta -= step
	}
	if delta >= step>>1 {
		nibble |= 2
		delta -= step >> 1
	}
	if delta >= step>>2 {
		nibble |= 1
		delta -= step >> 2
	}
	newPred, newIndex = imaReconstruct(predictor, index, nibble)
	return nibble, newPred, newIndex
}

// encodeIMA 把交错 PCM（16bit）编成 IMA ADPCM 的 data 段。
//
// 每块的第一个样本**不进 nibble**（它由块头里的 predictor 表达），所以
// samplesPerBlock 里含它 —— 与 `decodeIMA` 的取数一致。
func encodeIMA(pcm []int16, channels int) []byte {
	if channels < 1 {
		return nil
	}
	ba := imaBlockAlign(channels)
	perBlock := imaSamplesPerBlock(channels)
	frames := len(pcm) / channels
	out := make([]byte, 0, frames*channels/2+ba)

	for start := 0; start < frames; start += perBlock {
		n := min(perBlock, frames-start)
		pred := make([]int, channels)
		idx := make([]int, channels)
		// 块头：预测器 = 本块第一个样本；索引从 0 起（合法且简单）
		for c := 0; c < channels; c++ {
			pred[c] = int(pcm[start*channels+c])
			idx[c] = 0
			out = append(out, byte(pred[c]), byte(pred[c]>>8), 0, 0)
		}
		// nibble：低半字节先
		pos := len(out)
		out = append(out, make([]byte, ba-4*channels)...)
		pending := -1
		for i := 1; i < n; i++ {
			for c := 0; c < channels; c++ {
				s := int(pcm[(start+i)*channels+c])
				nb, p, ix := imaEncodeSample(pred[c], idx[c], s)
				pred[c], idx[c] = p, ix
				if pending < 0 {
					pending = nb
				} else {
					out[pos] = byte(pending | (nb << 4))
					pos++
					pending = -1
				}
			}
		}
		if pending >= 0 {
			out[pos] = byte(pending)
			pos++
		}
		// 本块最后一个样本可能落在 nibble 之外（帧数为奇数时）⇒ 补静音 nibble 到块尾
		for pos < len(out) {
			out[pos] = 0
			pos++
		}
	}
	return out
}

// decodeIMA 把 data 段解回交错 PCM（**工具自检用**；客户端里有一份等价的 Rust 实现）。
//
// ⚠️ 与 `encodeIMA` 严格镜像（同样的"块头 + 每帧每声道一个 nibble、低半字节先"），
// 两边有任何不一致都会在自检的 SNR 上暴露。
//
// 注意：**最后一块的补零会解出多余帧**（补的 0 nibble 不是"静音"而是"升一点"）——
// 所以调用方必须按真实帧数截断（WAV 的 `fact` 段就是干这个的）。
func decodeIMA(data []byte, channels int) []int16 {
	if channels < 1 {
		return nil
	}
	ba := imaBlockAlign(channels)
	perBlock := imaSamplesPerBlock(channels)
	var out []int16
	for off := 0; off+ba <= len(data); off += ba {
		block := data[off : off+ba]
		pred := make([]int, channels)
		idx := make([]int, channels)
		for c := 0; c < channels; c++ {
			pred[c] = int(int16(binary.LittleEndian.Uint16(block[4*c:])))
			idx[c] = int(block[4*c+2])
			if idx[c] > 88 {
				idx[c] = 88
			}
			out = append(out, int16(pred[c])) // 块头那个样本本身不计 nibble
		}
		nibbles := block[4*channels:]
		produced := 1 // 本块已产出的帧数（含块头那一个）
		ch := 0
		i := 0
		for produced < perBlock && i < len(nibbles) {
			b := nibbles[i]
			i++
			for _, nib := range [2]int{int(b & 0x0F), int(b >> 4)} {
				if produced >= perBlock {
					break
				}
				pred[ch], idx[ch] = imaReconstruct(pred[ch], idx[ch], nib)
				out = append(out, int16(pred[ch]))
				ch++
				if ch == channels {
					ch = 0
					produced++
				}
			}
		}
	}
	return out
}

// snrDB 解码与原始对比的 SNR（dB）—— 工具的自检指标。
//
// 4bit ADPCM 的典型 SNR 是 20~40 dB（依赖素材）：低于 15 dB 说明编码器写坏了。
func snrDB(orig, dec []int16) float64 {
	n := min(len(orig), len(dec))
	if n == 0 {
		return math.Inf(-1)
	}
	var sig, err float64
	for i := 0; i < n; i++ {
		s := float64(orig[i])
		d := float64(dec[i])
		sig += s * s
		err += (s - d) * (s - d)
	}
	if err == 0 {
		return math.Inf(1)
	}
	if sig == 0 {
		return math.Inf(-1)
	}
	return 10 * math.Log10(sig/err)
}
