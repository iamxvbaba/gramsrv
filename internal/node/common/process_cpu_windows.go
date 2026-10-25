//go:build windows

package common

import "golang.org/x/sys/windows"

func processCPUSeconds() (float64, bool) {
	handle, err := windows.GetCurrentProcess()
	if err != nil {
		return 0, false
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, false
	}
	// Kernel and user CPU times are FILETIME *durations* in 100ns ticks, not
	// wall-clock timestamps. Filetime.Nanoseconds() assumes the latter (it
	// subtracts the 1601→1970 epoch offset and multiplies by 100), which
	// overflows int64 for durations, so convert the raw ticks ourselves.
	ticks := filetimeTicks(kernel) + filetimeTicks(user)
	if ticks < 0 {
		return 0, false
	}
	return float64(ticks) / 1e7, true
}

func filetimeTicks(ft windows.Filetime) int64 {
	return int64(uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime))
}
