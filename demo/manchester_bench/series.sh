#!/bin/sh
# Measurement series: path × bit rate, 50 frames of 56 bytes each.
#
# Paths are those of the test bench in github.com/womat/gpio/demo/wirecheck;
# the optocouplers invert, so their receiver decodes with -thomas.
#
#   ./series.sh              normal priority
#   RT=1 ./series.sh         both processes with chrt -f 50 and GOGC=off
#   BIN=/tmp/manchester_bench ./series.sh
BIN=${BIN:-./manchester_bench}
N=50
run=""
if [ -n "$RT" ]; then
  export GOGC=off
  run="sudo -E chrt -f 50"
fi
for path in "direct 18 25 " "4N35 12 26 -thomas" "EL817 13 6 -thomas"; do
  set -- $path
  name=$1 out=$2 in=$3 opt=$4
  for rate in 500 1000 2000 3000 5000; do
    # duration: 576 bits per frame + 50 ms pause, plus a margin
    t=$(( (N * (576000 / rate + 50)) / 1000 + 4 ))
    echo "=== $name $rate bit/s"
    $run "$BIN" recv -in "$in" -rate "$rate" -n "$N" -t "${t}s" $opt &
    sleep 0.5
    $run "$BIN" send -out "$out" -rate "$rate" -n "$N"
    wait
  done
done
