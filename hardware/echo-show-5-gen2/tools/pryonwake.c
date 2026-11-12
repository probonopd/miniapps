/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

/*
 * Runs Amazon's wake word engine outside the Alexa apps.
 *
 *   pryonwake <pryon.manifest> [<capture.pcm> <seconds>] < 16 kHz mono S16LE > "ALEXA heard 14000", then "ALEXA accept 14000"
 *
 * With a capture file, the audio after an accepted word is saved there before the
 * word is reported: whoever acts on it would otherwise start too late to hear it.
 * It starts after the caller's beep and leaves the wake word out: in the capture it
 * would come out as words of the caller's grammar (Vosk hears "Alexa" as the letter X).
 *
 * The API layout comes from echo-dot-assist (MIT), src/include/pryon_api.h and
 * src/hassmic/wake_pryon.c. No NDK or bionic headers exist on the build host, so
 * only what is used is declared here.
 */

#include <stddef.h>
#include <stdint.h>

#define PRYON_MAX_CHANNELS 32

struct format {
	int32_t encoding, sample_rate, bits, channels;
	int32_t channel_types[PRYON_MAX_CHANNELS];
};

struct result {
	uint8_t pad0[8];
	uint64_t begin;
	uint8_t pad1[12];
	const char *keyword;
	uint8_t pad2[4];
	int32_t type;
	uint32_t metadata_size;
	const void *metadata;
};

const char *PryonApi_GetAttributes(void);
int PryonApi_SetLoggingCallback(void (*cb)(int, const char *, const char *));
int PryonApi_SetEnumeratedResultCallback(void (*cb)(const char *, struct result *));
int PryonModelSet_New(const char *id, const char *manifest, const char *situations);
void PryonDecoder_NewPryonMultichannelAudioFormat_Default(struct format *out);
int PryonDecoder_NewSpotterAudioDecoder(const char *id, const char *model_set, const char *model,
					struct format format, const char *config);
int PryonDecoder_PushAudioEventSamples(const char *id, uint64_t index, const int16_t *samples, uint32_t count);

int main(int argc, char **argv, char **envp);
int dprintf(int fd, const char *format, ...);
long read(int fd, void *buffer, size_t size);
size_t strlcpy(char *to, const char *from, size_t size);
int strcmp(const char *a, const char *b);
long write(int fd, const void *buffer, size_t size);
int open(const char *path, int flags, ...);
int close(int fd);

#define O_CREAT_TRUNC_WRONLY 01101

static const char *decoder = "pryonwake-decoder";
static char accepted[32];
static volatile int have_accepted;
static volatile uint64_t accepted_begin;

/* Speech over the muted speaker sits near -50 dBFS, the quiet room near -70. */
#define SPEECH_RMS 100
#define BLOCKS_OF_SILENCE_TO_STOP 35

/*
 * The capture handed to the recognizer starts after the beep (150 ms, heard here about 0.2 s after
 * the word, plus its echo in the room): counted as speech, it started the silence timer, and a
 * short pause after the wake word then cut the command off before it began. 22 blocks of 20 ms.
 */
#define SKIP_SAMPLES (16000 * 44 / 100)

/* The processed stream is quiet: about +15 dB, as echo-dot-assist found. */
#define CAPTURE_GAIN 6

static void on_log(int level, const char *tag, const char *message)
{
	/* Levels 4 and 5 are hundreds of lines per model load. */
	if (level <= 3) {
		dprintf(2, "pryon %d %s: %s\n", level, tag ? tag : "", message ? message : "");
	}
}

static long rms(const int16_t *samples, uint32_t count)
{
	long long sum = 0;
	long root = 0;

	for (uint32_t i = 0; i < count; i++) {
		sum += (long)samples[i] * samples[i];
	}
	sum /= count ? count : 1;
	while ((long long)root * root < sum) {
		root += 4;
	}
	return root;
}

static void write_loud(int fd, const int16_t *in, uint32_t count)
{
	static int16_t out[320];

	for (uint32_t i = 0; i < count; i++) {
		int v = in[i] * CAPTURE_GAIN;
		out[i] = v > 32767 ? 32767 : v < -32768 ? -32768 : v;
	}
	write(fd, out, count * 2);
}

static void on_result(const char *id, struct result *r)
{
	(void)id;
	/* STOP is the engine's second word: it only means something to Alexa. */
	if (r->type == 2 && strcmp(r->keyword, "STOP") != 0) {
		/* The engine frees the keyword after this callback. */
		strlcpy(accepted, r->keyword, sizeof(accepted));
		accepted_begin = r->begin;
		have_accepted = 1;
	}
}

int main(int argc, char **argv, char **envp)
{
	(void)envp;
	struct format format;
	static int16_t samples[320];
	uint64_t index = 0, delay = 0;
	long got, left = 0;
	int spoken = 0, silent = 0;
	long skip = 0;
	int rc, capture = -1;
	const char *word = NULL;

	if (argc != 2 && argc != 4) {
		dprintf(2, "usage: pryonwake <pryon.manifest> [<capture.pcm> <seconds>]\n");
		return 2;
	}
	PryonApi_SetLoggingCallback(on_log);
	PryonApi_SetEnumeratedResultCallback(on_result);
	dprintf(2, "attributes: %s\n", PryonApi_GetAttributes());
	rc = PryonModelSet_New("pryonwake-models", argv[1], "");
	if (rc != 0) {
		dprintf(2, "PryonModelSet_New failed: %d\n", rc);
		return 1;
	}
	PryonDecoder_NewPryonMultichannelAudioFormat_Default(&format);
	rc = PryonDecoder_NewSpotterAudioDecoder(decoder, "pryonwake-models", "pryon", format, "{}");
	if (rc != 0) {
		dprintf(2, "PryonDecoder_NewSpotterAudioDecoder failed: %d\n", rc);
		return 1;
	}
	dprintf(2, "listening\n");
	while ((got = read(0, samples, sizeof(samples))) > 0) {
		uint32_t count = (uint32_t)got / 2;
		rc = PryonDecoder_PushAudioEventSamples(decoder, index, samples, count);
		if (rc != 0) {
			dprintf(2, "push failed: %d\n", rc);
			return 1;
		}
		index += count;
		if (have_accepted && left == 0) {
			word = accepted;
			if (argc == 2) {
				dprintf(1, "%s accept %llu\n", word, (unsigned long long)(index - accepted_begin));
			} else {
				capture = open(argv[2], O_CREAT_TRUNC_WRONLY, 0644);
				if (capture < 0) {
					dprintf(2, "cannot create %s\n", argv[2]);
					return 1;
				}
				left = 16000L * (argv[3][0] - '0');
				delay = index - accepted_begin;
				spoken = 0;
				silent = 0;
				skip = SKIP_SAMPLES;
				/* Reported at once: the caller can silence the speaker for the capture. */
				dprintf(1, "%s heard %llu\n", word, (unsigned long long)delay);
			}
			have_accepted = 0;
		} else if (left > 0 && skip > 0) {
			have_accepted = 0;
			skip -= count;
			left -= count;
		} else if (left > 0) {
			have_accepted = 0;
			write_loud(capture, samples, count);
			left -= count;
			/* Every second of audio costs most of a second of recognition. */
			if (rms(samples, count) >= SPEECH_RMS) {
				spoken = 1;
				silent = 0;
			} else if (spoken && ++silent >= BLOCKS_OF_SILENCE_TO_STOP) {
				left = 0;
			}
			if (left <= 0) {
				left = 0;
				close(capture);
				dprintf(1, "%s accept %llu\n", word, (unsigned long long)delay);
			}
		}
	}
	return 0;
}

/* Replaces the NDK's crtbegin_dynamic: bionic's __libc_init runs main. */
struct structors {
	void (**preinit)(void);
	void (**init)(void);
	void (**fini)(void);
};

void __libc_init(void *args, void (*onexit)(void), int (*entry)(int, char **, char **), const struct structors *s);

void start_main(void *args)
{
	static const struct structors none = { NULL, NULL, NULL };
	__libc_init(args, NULL, main, &none);
}

__asm__(".text\n.global _start\n.type _start,%function\n_start:\n  mov r0,sp\n  b start_main\n");
