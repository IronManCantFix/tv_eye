package tvmonitor

type State int

const (
	StateOff       State = iota
	StatePending
	StateTriggered
	StateResting
)

func (s State) String() string {
	switch s {
	case StatePending:
		return "PENDING"
	case StateTriggered:
		return "TRIGGERED"
	case StateResting:
		return "RESTING"
	default:
		return "OFF"
	}
}

type StateMachine struct {
	state          State
	onFrameCount   int
	offFrameCount  int
	debounceCount  int
	triggerFrames  int
	triggeredCount int
	restStartSet   bool
}

func NewStateMachine(debounceCount, triggerFrames int) *StateMachine {
	return &StateMachine{
		state:         StateOff,
		debounceCount: debounceCount,
		triggerFrames: triggerFrames,
	}
}

func (sm *StateMachine) Update(rawOn bool) (newState State, shouldTrigger bool) {
	if rawOn {
		sm.offFrameCount = 0
		sm.onFrameCount++
	} else {
		sm.onFrameCount = 0
		sm.offFrameCount++
	}

	switch sm.state {
	case StateOff:
		if sm.onFrameCount >= sm.debounceCount {
			sm.state = StatePending
			sm.onFrameCount = sm.debounceCount
		}

	case StatePending:
		if sm.offFrameCount >= sm.debounceCount {
			sm.state = StateOff
			sm.onFrameCount = 0
		} else if sm.onFrameCount >= sm.triggerFrames {
			sm.state = StateTriggered
			sm.triggeredCount = 0
			shouldTrigger = true
		}

	case StateTriggered:
		sm.triggeredCount++
		exceeded := sm.triggeredCount >= sm.triggerFrames
		tvOff := sm.offFrameCount >= sm.debounceCount
		if exceeded || tvOff {
			sm.state = StateResting
			sm.onFrameCount = 0
			sm.offFrameCount = 0
			sm.triggeredCount = 0
			sm.restStartSet = false
			shouldTrigger = exceeded
		}

	case StateResting:
		// RESTING only exits via ForceOff() (called by monitor when rest period elapses).
		// Do NOT transition based on TV on/off -- the rest duration must be enforced.
	}

	return sm.state, shouldTrigger
}

func (sm *StateMachine) ForceResting() {
	sm.state = StateResting
	sm.onFrameCount = 0
	sm.offFrameCount = 0
	sm.triggeredCount = 0
	sm.restStartSet = false
}

func (sm *StateMachine) ForceOff() {
	sm.state = StateOff
	sm.onFrameCount = 0
	sm.offFrameCount = 0
	sm.triggeredCount = 0
}

func (sm *StateMachine) State() State {
	return sm.state
}

func (sm *StateMachine) SetRestStartSet(v bool) {
	sm.restStartSet = v
}

func (sm *StateMachine) RestStartSet() bool {
	return sm.restStartSet
}

func (sm *StateMachine) OnFrameCount() int {
	return sm.onFrameCount
}
