/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import "math"

// Color conversions ported from HA's homeassistant/util/color.py so that
// derived attributes (rgb_color, hs_color, xy_color) match HA bit for bit.

func pyround(f float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.RoundToEven(f*p) / p
}

func rgbToXY(r, g, b float64) (float64, float64) {
	if r+g+b == 0 {
		return 0, 0
	}
	gamma := func(c float64) float64 {
		c /= 255
		if c > 0.04045 {
			return math.Pow((c+0.055)/1.055, 2.4)
		}
		return c / 12.92
	}
	R, G, B := gamma(r), gamma(g), gamma(b)
	X := R*0.664511 + G*0.154324 + B*0.162028
	Y := R*0.283881 + G*0.668433 + B*0.047685
	Z := R*0.000088 + G*0.072310 + B*0.986039
	return pyround(X/(X+Y+Z), 3), pyround(Y/(X+Y+Z), 3)
}

func xyToRGB(x, y float64) [3]int {
	if y == 0 {
		y += 0.00000000001
	}
	Y := 1.0
	X := (Y / y) * x
	Z := (Y / y) * (1 - x - y)
	r := X*1.656492 - Y*0.354851 - Z*0.255038
	g := -X*0.707196 + Y*1.655397 + Z*0.036152
	b := X*0.051713 - Y*0.121364 + Z*1.011530
	inv := func(c float64) float64 {
		if c <= 0.0031308 {
			return 12.92 * c
		}
		return 1.055*math.Pow(c, 1/2.4) - 0.055
	}
	r, g, b = math.Max(0, inv(r)), math.Max(0, inv(g)), math.Max(0, inv(b))
	if m := math.Max(r, math.Max(g, b)); m > 1 {
		r, g, b = r/m, g/m, b/m
	}
	return [3]int{int(r * 255), int(g * 255), int(b * 255)}
}

func rgbToHS(r, g, b float64) (float64, float64) {
	r, g, b = r/255, g/255, b/255
	maxc := math.Max(r, math.Max(g, b))
	minc := math.Min(r, math.Min(g, b))
	if maxc == minc {
		return 0, 0
	}
	s := (maxc - minc) / maxc
	rc := (maxc - r) / (maxc - minc)
	gc := (maxc - g) / (maxc - minc)
	bc := (maxc - b) / (maxc - minc)
	var h float64
	switch {
	case r == maxc:
		h = bc - gc
	case g == maxc:
		h = 2 + rc - bc
	default:
		h = 4 + gc - rc
	}
	h = math.Mod(h/6, 1)
	if h < 0 {
		h += 1
	}
	return pyround(h*360, 3), pyround(s*100, 3)
}

func hsToRGB(h, s float64) [3]int {
	hh, ss, v := h/360, s/100, 1.0
	if ss == 0 {
		return [3]int{255, 255, 255}
	}
	i := int(hh * 6)
	f := hh*6 - float64(i)
	p := v * (1 - ss)
	q := v * (1 - ss*f)
	t := v * (1 - ss*(1-f))
	var r, g, b float64
	switch i % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return [3]int{int(r * 255), int(g * 255), int(b * 255)}
}

func colorTempToRGB(kelvin float64) [3]int {
	kelvin = math.Max(1000, math.Min(40000, kelvin))
	t := kelvin / 100
	clamp := func(v float64) float64 { return math.Max(0, math.Min(255, v)) }
	var r, g, b float64
	if t <= 66 {
		r = 255
		g = clamp(99.4708025861*math.Log(t) - 161.1195681661)
	} else {
		r = clamp(329.698727446 * math.Pow(t-60, -0.1332047592))
		g = clamp(288.1221695283 * math.Pow(t-60, -0.0755148492))
	}
	switch {
	case t >= 66:
		b = 255
	case t <= 19:
		b = 0
	default:
		b = clamp(138.5177312231*math.Log(t-10) - 305.0447927307)
	}
	return [3]int{int(r), int(g), int(b)}
}

func miredToKelvin(m float64) int { return int(math.Floor(1000000 / m)) }
func kelvinToMired(k float64) int { return int(math.Floor(1000000 / k)) }
