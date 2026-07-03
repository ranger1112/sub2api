// Package eventstream 实现 AWS Event Stream (application/vnd.amazon.eventstream)
// 二进制帧协议的流式解码,用于解析 Kiro / Amazon Q 的 generateAssistantResponse
// 端点返回的响应流。
//
// 帧格式:
//
//		┌──────────────┬───────────────┬─────────────┬─────────┬─────────┬───────────┐
//		│ Total Length │ Header Length │ Prelude CRC │ Headers │ Payload │  Msg CRC  │
//		│   4 bytes    │    4 bytes    │   4 bytes   │  变长   │  变长   │  4 bytes  │
//		└──────────────┴───────────────┴─────────────┴─────────┴─────────┴───────────┘
//
//	  - Total Length:整条消息长度(含自身 4 字节)
//	  - Prelude CRC: 前 8 字节(Total Length + Header Length)的 CRC32
//	  - Msg CRC:     除最后 4 字节外全部内容的 CRC32
//
// CRC 采用 CRC-32 / ISO-HDLC(以太网/ZIP 标准,多项式 0xEDB88320),
// 等价于 Go 标准库的 crc32.IEEE。
//
// 本包移植自 kiro.rs 的 src/kiro/parser 分层实现(crc / header / frame / decoder),
// 行为保持一致:parseFrame 为无状态纯函数,Decoder 负责缓冲管理与容错恢复。
package eventstream
