#!/bin/zsh
# build-clip.sh <name> <mobile|desktop> <workdir> <outdir>: <workdir>/<name>-<kind>.frames + .marks.json (from record-clip.cjs) -> <outdir>/<name>-<kind>{,-captioned}.mp4 (60 fps H.264 CRF 18) and -captioned.gif (<= 8 MB).
# Caption windows start at each mark (first at 0.15 s, the load lead trimmed) and last as long as the DA-cleared clip's did (timing/<name>-<kind>.timing.json).
name=$1; kind=$2; wd=$3; out=$4; here=${0:A:h}
mkdir -p $out; base=$name-$kind; marks=$wd/$base.marks.json
if [ $kind = mobile ]; then W=1080; H=1920; gw=320; else W=1920; H=1080; gw=640; fi
PW_MODULE=${PW_MODULE:-playwright} node $here/make-caption-pngs.cjs $name $kind $wd/$base.caps || exit 1
python3 - "$marks" "$here/timing/$base.timing.json" "$wd/$base.win.txt" "$here/timing/durations.json" "$name" <<'P' || exit 1
import json,sys,os
m=json.load(open(sys.argv[1]))
if os.path.exists(sys.argv[2]): old=json.load(open(sys.argv[2]))['captionWindows']
else:  # a clip without cleared timing: caption lengths from durations.json
    dur=json.load(open(sys.argv[4]))[sys.argv[5]];old=[{'caption_start_sec':0,'caption_end_sec':d} for d in dur]
t0=max(0,m['marks'][0]['t']-0.15)
with open(sys.argv[3],'w') as f:
    f.write(f"{t0:.2f}\n")
    for i,mk in enumerate(m['marks']):
        s=0.15 if i==0 else mk['t']-t0
        d=old[i]['caption_end_sec']-old[i]['caption_start_sec']
        f.write(f"{s:.2f} {s+d:.2f}\n")
P
ss=$(head -1 $wd/$base.win.txt); n=$(($(wc -l < $wd/$base.win.txt)-1))
ins=(); fc="[0:v]fps=60,trim=start=$ss,setpts=PTS-STARTPTS,scale=${W}:${H}:flags=lanczos,format=yuv420p[v0]"; prev=v0; i=0
for l in ${(f)"$(tail -n +2 $wd/$base.win.txt)"}; do
  a=${l%% *}; b=${l##* }; ins+=(-i $wd/$base.caps/cap$i.png)
  fc="$fc;[$prev][$((i+1)):v]overlay=0:0:enable='between(t,$a,$b)'[v$((i+1))]"; prev=v$((i+1)); i=$((i+1))
done
# Platforms re-encode uploads, so keep the source clean: CRF 10, animation tune, capped at 16 Mbps. (x264 does not honour -minrate without CBR; a padded 8 Mbps floor would add size, not detail, on static UI.)
enc=(-c:v libx264 -preset slow -tune animation -crf ${CRF:-10} -maxrate 16M -bufsize 16M -pix_fmt yuv420p -r 60 -movflags +faststart -an)
ffmpeg -v error -y -f concat -safe 0 -i $wd/$base.frames/frames.ffconcat $ins -filter_complex "$fc" -map "[$prev]" $enc $out/$base-captioned.mp4 || exit 1
ffmpeg -v error -y -f concat -safe 0 -i $wd/$base.frames/frames.ffconcat -vf "fps=60,trim=start=$ss,setpts=PTS-STARTPTS,scale=${W}:${H}:flags=lanczos,format=yuv420p" $enc $out/$base.mp4 || exit 1
for fps in 12 10 8; do for w in $((gw*3/2)) $gw; do
  ffmpeg -v error -y -i $out/$base-captioned.mp4 -vf "fps=$fps,scale=$w:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128[p];[b][p]paletteuse=dither=bayer:bayer_scale=4" -loop 0 $out/$base-captioned.gif
  sz=$(stat -f%z $out/$base-captioned.gif); if [ $sz -le 8000000 ]; then echo "$base: mp4 $(stat -f%z $out/$base-captioned.mp4) B, gif $sz B (${w}px @ ${fps}fps)"; exit 0; fi
done; done
echo "$base: gif above 8 MB at the lowest setting"; exit 1
