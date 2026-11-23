#ifndef SILENCECUT_FFMPEG_H
#define SILENCECUT_FFMPEG_H

#include <stdint.h>

typedef struct CutInput CutInput;
typedef struct CutOutput CutOutput;

enum { CUT_AUDIO_ONLY, CUT_SOFTWARE, CUT_HARDWARE, CUT_DIRECT };

typedef struct {
    int64_t duration;
    int rate, channels, video, width, height;
} CutInfo;

typedef struct {
    int64_t pts, silence_start, silence_end;
    int samples;
    const float *audio;
} CutFrame;

CutInput *cut_open(const char *path, int mode, CutInfo *info, int *err);
void cut_close(CutInput *in);
void cut_seek(CutInput *in, int64_t time);
int cut_detect(CutInput *in, double level);
int cut_next(CutInput *in, CutFrame *frame);
int cut_hold_video(CutInput *in);
void cut_error(int code, char *buf, int size);

CutOutput *cut_create(CutInput *in, const char *path, int proxy, int *err);
int cut_audio(CutOutput *out, const float *data, int samples);
int cut_video(CutOutput *out, CutInput *in, int64_t pts, int64_t duration);
int cut_finish(CutOutput *out);
void cut_discard(CutOutput *out);

#endif
