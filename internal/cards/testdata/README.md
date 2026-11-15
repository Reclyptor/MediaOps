Transcripts of real ffmpeg 9.0.2 output, captured with the argv mkv-cards runs.
The clip is 320x180, 25 fps: 6 s testsrc2, a 5 s held 0xB01818 card with a white box,
5 s testsrc2, then a 4 s held card that runs to the end of the 20 s input.

signalstats-clip.txt: `-ss 0 -t 20 ... -vf fps=1,signalstats,metadata=print -f null -`,
trimmed to the frames at 5..12 s. The card is 6..10 s.

freezedetect-closed.txt: `-ss 0 -t 14 ... -vf freezedetect=n=-30dB:d=3.5`; the freeze at 6 s
ends inside the window, so start, duration and end are all printed.

freezedetect-open.txt: the same over `-t 20`. The card at 16 s is still held when the input ends:
ffmpeg prints freeze_start and then nothing, no duration, no end.
