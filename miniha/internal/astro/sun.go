/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package astro computes sun times and positions with the NOAA solar
// calculator formulas (what HA's astral dependency implements), accurate
// to about a minute.
package astro

import (
	"math"
	"time"
)

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

// solar holds the intermediate quantities for one instant.
type solar struct {
	declination float64 // degrees
	eqTime      float64 // minutes
}

func compute(t time.Time) solar {
	jd := float64(t.UTC().Unix())/86400 + 2440587.5
	jc := (jd - 2451545) / 36525
	geomMeanLong := math.Mod(280.46646+jc*(36000.76983+jc*0.0003032), 360)
	geomMeanAnom := 357.52911 + jc*(35999.05029-0.0001537*jc)
	eccent := 0.016708634 - jc*(0.000042037+0.0000001267*jc)
	sunEqCtr := math.Sin(rad(geomMeanAnom))*(1.914602-jc*(0.004817+0.000014*jc)) + math.Sin(rad(2*geomMeanAnom))*(0.019993-0.000101*jc) + math.Sin(rad(3*geomMeanAnom))*0.000289
	trueLong := geomMeanLong + sunEqCtr
	appLong := trueLong - 0.00569 - 0.00478*math.Sin(rad(125.04-1934.136*jc))
	meanObliq := 23 + (26+(21.448-jc*(46.815+jc*(0.00059-jc*0.001813)))/60)/60
	obliqCorr := meanObliq + 0.00256*math.Cos(rad(125.04-1934.136*jc))
	declin := deg(math.Asin(math.Sin(rad(obliqCorr)) * math.Sin(rad(appLong))))
	varY := math.Tan(rad(obliqCorr/2)) * math.Tan(rad(obliqCorr/2))
	eqTime := 4 * deg(varY*math.Sin(2*rad(geomMeanLong))-2*eccent*math.Sin(rad(geomMeanAnom))+4*eccent*varY*math.Sin(rad(geomMeanAnom))*math.Cos(2*rad(geomMeanLong))-0.5*varY*varY*math.Sin(4*rad(geomMeanLong))-1.25*eccent*eccent*math.Sin(2*rad(geomMeanAnom)))
	return solar{declin, eqTime}
}

// SunTimes returns sunrise and sunset for the civil day of t at the
// given location; ok is false in polar day or night.
func SunTimes(t time.Time, lat, lon float64) (rise, set time.Time, ok bool) {
	y, m, d := t.Date()
	loc := t.Location()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
	s := compute(midnight)
	cosHA := math.Cos(rad(90.833))/(math.Cos(rad(lat))*math.Cos(rad(s.declination))) - math.Tan(rad(lat))*math.Tan(rad(s.declination))
	if cosHA < -1 || cosHA > 1 {
		return time.Time{}, time.Time{}, false
	}
	ha := deg(math.Acos(cosHA))
	solarNoonMin := 720 - 4*lon - s.eqTime // minutes after UTC midnight
	utcMidnight := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	rise = utcMidnight.Add(time.Duration((solarNoonMin - ha*4) * float64(time.Minute))).In(loc)
	set = utcMidnight.Add(time.Duration((solarNoonMin + ha*4) * float64(time.Minute))).In(loc)
	return rise, set, true
}

// NextSunEvent finds the next sunrise or sunset after t, plus offset.
func NextSunEvent(t time.Time, lat, lon float64, event string, offset time.Duration) (time.Time, bool) {
	for day := 0; day < 3; day++ {
		rise, set, ok := SunTimes(t.AddDate(0, 0, day), lat, lon)
		if !ok {
			continue
		}
		at := rise
		if event == "sunset" {
			at = set
		}
		at = at.Add(offset)
		if at.After(t) {
			return at, true
		}
	}
	return time.Time{}, false
}

// Position returns the sun's elevation and azimuth in degrees at t.
func Position(t time.Time, lat, lon float64) (elevation, azimuth float64) {
	s := compute(t)
	u := t.UTC()
	minutes := float64(u.Hour()*60+u.Minute()) + float64(u.Second())/60
	trueSolarTime := math.Mod(minutes+s.eqTime+4*lon+1440, 1440)
	hourAngle := trueSolarTime/4 - 180
	if trueSolarTime/4 < 0 {
		hourAngle = trueSolarTime/4 + 180
	}
	zenith := deg(math.Acos(math.Sin(rad(lat))*math.Sin(rad(s.declination)) + math.Cos(rad(lat))*math.Cos(rad(s.declination))*math.Cos(rad(hourAngle))))
	elevation = 90 - zenith
	// Atmospheric refraction correction as in the NOAA sheet.
	var refr float64
	switch {
	case elevation > 85:
		refr = 0
	case elevation > 5:
		te := math.Tan(rad(elevation))
		refr = 58.1/te - 0.07/math.Pow(te, 3) + 0.000086/math.Pow(te, 5)
	case elevation > -0.575:
		refr = 1735 + elevation*(-518.2+elevation*(103.4+elevation*(-12.79+elevation*0.711)))
	default:
		refr = -20.772 / math.Tan(rad(elevation))
	}
	elevation += refr / 3600
	az := deg(math.Acos(((math.Sin(rad(lat)) * math.Cos(rad(zenith))) - math.Sin(rad(s.declination))) / (math.Cos(rad(lat)) * math.Sin(rad(zenith)))))
	if hourAngle > 0 {
		azimuth = math.Mod(az+180, 360)
	} else {
		azimuth = math.Mod(540-az, 360)
	}
	return elevation, azimuth
}
