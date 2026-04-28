//go:build !linux

package service

func getNetMaxSpeedViaIoctl(ifname string) (int, error) {
	return 0, nil
}
