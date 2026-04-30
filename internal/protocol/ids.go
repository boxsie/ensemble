package protocol

const (
	ProtocolSignaling    = "/ensemble/signaling/1.0.0"
	ProtocolChat         = "/ensemble/chat/1.0.0"
	ProtocolFileTransfer = "/ensemble/filetransfer/1.0.0"
	ProtocolPresence     = "/ensemble/presence/1.0.0"
	ProtocolDHT          = "/ensemble/dht/1.0.0"

	MaxMessageSize = 16 * 1024 * 1024 // 16 MB
)
