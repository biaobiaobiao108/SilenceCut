#ifndef SILENCECUT_FFMPEG_H
#define SILENCECUT_FFMPEG_H

#include <stdint.h>

typedef struct CutInput CutInput;
typedef struct CutOutput CutOutput;

enum { CUT_AUDIO_ONLY, CUT_SOFTWARE, CUT_HARDWARE_ENCODER, CUT_HARDWARE, CUT_DIRECT };
enum { CUT_VIDEO_ENCODE_ERROR = 1, CUT_VIDEO_DECODE_ERROR };

typedef struct {
    int64_t duration;
    int rate, channels, video, width, height, hardware_decoder;
} CutInfo;

typedef struct {
    int64_t pts;
    int samples, video_error, seek_error;
    const float *audio;
} CutFrame;

CutInput *cut_open(const char *path, int mode, CutInfo *info, int *err);
void cut_close(CutInput *in);
int cut_skip_gap(CutInput *in, int64_t after, int64_t before, int initial);
int cut_next(CutInput *in, CutFrame *frame);
int cut_hold_video(CutInput *in);
void cut_error(int code, char *buf, int size);

CutOutput *cut_create(CutInput *in, const char *path, int proxy, int *err, int *video_error);
int cut_audio(CutOutput *out, const float *data, int samples);
int cut_video(CutOutput *out, CutInput *in, int64_t pts, int64_t duration, int *video_error);
int cut_finish(CutOutput *out, int *video_error);
void cut_discard(CutOutput *out);

#endif
