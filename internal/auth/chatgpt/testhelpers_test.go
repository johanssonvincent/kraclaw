package chatgpt

import "time"

func deviceCodeForTest(authID, userCode, verifyURL string, interval time.Duration) *DeviceCode {
	return &DeviceCode{
		deviceAuthID:    authID,
		UserCode:        userCode,
		VerificationURL: verifyURL,
		Interval:        interval,
	}
}
