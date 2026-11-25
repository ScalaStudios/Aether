package api

import "time"

type availabilityWindow struct {
	Weekday     int
	StartMinute int
	EndMinute   int
}

type timeInterval struct {
	Start time.Time
	End   time.Time
}

func slotStep(duration int) int {
	switch {
	case duration >= 15 && duration%15 == 0:
		return 15
	case duration%5 == 0:
		return 5
	default:
		return duration
	}
}

func bookingSlots(now, from, to time.Time, loc *time.Location, duration, bufferBefore, bufferAfter, minNoticeMin, horizonDays int, windows []availabilityWindow, busy []timeInterval) []time.Time {
	if loc == nil || duration <= 0 || !to.After(from) || len(windows) == 0 {
		return []time.Time{}
	}
	step := slotStep(duration)
	earliest := now.Add(time.Duration(minNoticeMin) * time.Minute)
	if from.After(earliest) {
		earliest = from
	}
	latest := now.AddDate(0, 0, horizonDays)
	if to.Before(latest) {
		latest = to
	}
	if !latest.After(earliest) {
		return []time.Time{}
	}
	out := []time.Time{}
	startDay := from.In(loc)
	endDay := to.In(loc)
	first := time.Date(startDay.Year(), startDay.Month(), startDay.Day(), 0, 0, 0, 0, loc)
	last := time.Date(endDay.Year(), endDay.Month(), endDay.Day(), 0, 0, 0, 0, loc)
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		weekday := int(day.Weekday())
		for _, w := range windows {
			if w.Weekday != weekday || w.EndMinute-w.StartMinute < duration {
				continue
			}
			for minute := w.StartMinute; minute+duration <= w.EndMinute; minute += step {
				slot := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, loc)
				wall := slot.In(loc)
				wallMin := wall.Hour()*60 + wall.Minute()
				if wall.Year() != day.Year() || wall.Month() != day.Month() || wall.Day() != day.Day() || wallMin != minute {
					continue
				}
				end := slot.Add(time.Duration(duration) * time.Minute)
				if slot.Before(earliest) || !slot.Before(latest) || slotBlocked(slot, end, busy, bufferBefore, bufferAfter) {
					continue
				}
				out = append(out, slot.UTC())
				if len(out) >= 1000 {
					return out
				}
			}
		}
	}
	return out
}

func slotBlocked(start, end time.Time, busy []timeInterval, bufferBefore, bufferAfter int) bool {
	before := time.Duration(bufferBefore) * time.Minute
	after := time.Duration(bufferAfter) * time.Minute
	for _, ev := range busy {
		if !ev.End.After(ev.Start) {
			continue
		}
		if start.Before(ev.End.Add(before)) && end.After(ev.Start.Add(-after)) {
			return true
		}
	}
	return false
}
