package bridge

import (
	"fmt"
	"time"
)

var ptzStopRetryDelays = [2]time.Duration{250 * time.Millisecond, 500 * time.Millisecond}

// retryPTZStop checks ownership before every attempt, including after a delay.
// Callers must release their channel lock before invoking it.
func retryPTZStop(tag, camera string, allowed func() bool, send func() error, delays [2]time.Duration) error {
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if !allowed() {
			return err
		}
		// A newer move can arrive after the check; a stale stop is accepted to err toward stopping.
		err = send()
		if err != nil {
			fmt.Printf("[%s] PTZ stop failed camera=%s attempt=%d/3: %v\n", tag, camera, attempt, err)
			if attempt == 3 {
				fmt.Printf("[%s] PTZ stop exhausted camera=%s; camera may still be moving: %v\n", tag, camera, err)
				return err
			}
		} else {
			return nil
		}
		time.Sleep(delays[attempt-1])
	}
	return err
}
