package ssh

const (
	SSH_AGENTC_REQUEST_IDENTITIES = 11
	SSH_AGENT_IDENTITIES_ANSWER   = 12
	SSH_AGENTC_SIGN_REQUEST       = 13
	SSH_AGENT_SIGN_RESPONSE       = 14
	SSH_AGENTC_ADD_IDENTITY      = 17
	SSH_AGENTC_REMOVE_IDENTITY   = 18
	SSH_AGENTC_REMOVE_ALL_IDENTITIES = 19
	SSH_AGENT_FAILURE             = 5
	SSH_AGENT_SUCCESS             = 6
)

type AgentMessage struct {
	Type uint8
	Data []byte
}

func ReadAgentMessage(data []byte) (*AgentMessage, error) {
	if len(data) < 1 {
		return nil, nil
	}
	return &AgentMessage{
		Type: data[0],
		Data: data[1:],
	}, nil
}

func EncodeAgentMessage(msg *AgentMessage) []byte {
	result := make([]byte, 1+len(msg.Data))
	result[0] = msg.Type
	copy(result[1:], msg.Data)
	return result
}
