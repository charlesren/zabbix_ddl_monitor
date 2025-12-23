package connection

// ConnectionState 连接状态枚举
type ConnectionState int

const (
	// StateIdle 空闲状态：连接可用，未被使用
	StateIdle ConnectionState = iota
	// StateAcquired 已获取状态：连接已被获取，正在使用
	StateAcquired
	// StateChecking 检查状态：连接正在接受健康检查
	StateChecking
	// StateRebuilding 重建状态：连接正在被重建
	StateRebuilding
	// StateClosing 关闭中状态：连接正在关闭
	StateClosing
	// StateClosed 已关闭状态：连接已关闭
	StateClosed
)

// String 返回连接状态的字符串表示
func (s ConnectionState) String() string {
	switch s {
	case StateIdle:
		return "Idle"
	case StateAcquired:
		return "Acquired"
	case StateChecking:
		return "Checking"
	case StateRebuilding:
		return "Rebuilding"
	case StateClosing:
		return "Closing"
	case StateClosed:
		return "Closed"
	default:
		return "Unknown"
	}
}

// CanTransition 检查是否可以从当前状态转换到目标状态
func CanTransition(currentState, targetState ConnectionState) bool {
	// 状态转换规则
	switch currentState {
	case StateIdle:
		// 空闲状态可以转换到：已获取、检查中、重建中、关闭中、已关闭
		return targetState == StateAcquired || targetState == StateChecking ||
			targetState == StateRebuilding || targetState == StateClosing ||
			targetState == StateClosed
	case StateAcquired:
		// 已获取状态可以转换到：空闲、检查中、关闭中、已关闭
		return targetState == StateIdle || targetState == StateChecking ||
			targetState == StateClosing || targetState == StateClosed
	case StateChecking:
		// 检查中状态可以转换到：空闲、已获取、关闭中、已关闭
		return targetState == StateIdle || targetState == StateAcquired ||
			targetState == StateClosing || targetState == StateClosed
	case StateRebuilding:
		// 重建中状态可以转换到：空闲、关闭中、已关闭
		return targetState == StateIdle || targetState == StateClosing ||
			targetState == StateClosed
	case StateClosing:
		// 关闭中状态只能转换到：已关闭
		return targetState == StateClosed
	case StateClosed:
		// 已关闭状态不能转换到任何其他状态
		return false
	default:
		return false
	}
}

// GetValidTransitions 获取当前状态的有效转换目标状态列表
func GetValidTransitions(currentState ConnectionState) []ConnectionState {
	var validStates []ConnectionState

	// 检查所有可能的目标状态
	for targetState := StateIdle; targetState <= StateClosed; targetState++ {
		if CanTransition(currentState, targetState) {
			validStates = append(validStates, targetState)
		}
	}

	return validStates
}

// IsTerminalState 检查是否为终止状态
func IsTerminalState(state ConnectionState) bool {
	return state == StateClosed
}

// IsOperationalState 检查是否为可操作状态
func IsOperationalState(state ConnectionState) bool {
	return state == StateIdle || state == StateAcquired || state == StateChecking
}

// IsTransitionalState 检查是否为过渡状态
func IsTransitionalState(state ConnectionState) bool {
	return state == StateRebuilding || state == StateClosing
}
