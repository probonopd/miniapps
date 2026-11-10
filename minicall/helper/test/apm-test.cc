/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
// Link smoke test for build-apm.sh: the exact feature set baresip's webrtc_aec uses.
#include <cstdint>
#include <cstdio>
#include <vector>
#include "modules/audio_processing/include/audio_processing.h"

int main()
{
	webrtc::AudioProcessing::Config cfg;
	cfg.echo_canceller.enabled = true;
	cfg.noise_suppression.enabled = true;
	cfg.noise_suppression.level =
		webrtc::AudioProcessing::Config::NoiseSuppression::kHigh;
	cfg.high_pass_filter.enabled = true;
	cfg.gain_controller2.enabled = true;
	cfg.gain_controller2.adaptive_digital.enabled = true;

	rtc::scoped_refptr<webrtc::AudioProcessing> apm =
		webrtc::AudioProcessingBuilder().Create();
	if (!apm) {
		std::puts("create failed");
		return 1;
	}
	apm->ApplyConfig(cfg);

	webrtc::StreamConfig sc(48000, 1);
	std::vector<int16_t> in(480, 0), out(480, 0);
	int err = apm->ProcessStream(in.data(), sc, sc, out.data());
	if (err != webrtc::AudioProcessing::kNoError) {
		std::printf("ProcessStream failed: %d\n", err);
		return 1;
	}
	std::puts("apm ok");
	return 0;
}
