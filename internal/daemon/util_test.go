package daemon

import "time"

func timeSleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }
