//go:build !linux && !darwin && !windows

package sysinfo

import "fmt"

func platformStatic() (StaticInfo, error) {
	return StaticInfo{}, fmt.Errorf("static telemetry not implemented for this OS")
}

func platformDynamic() (DynamicInfo, error) {
	return DynamicInfo{}, fmt.Errorf("dynamic telemetry not implemented for this OS")
}
