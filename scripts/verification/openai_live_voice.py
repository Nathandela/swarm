"""Opt-in, billed speech check using the configured CLI and synthetic audio only."""
import argparse
import array
import json
import pathlib
import subprocess
import sys
import tempfile
import wave


def transcribe(binary, pcm):
    result = subprocess.run([binary, "voice", "transcribe"], input=pcm,
                            stdout=subprocess.PIPE, check=True, timeout=45)
    return result.stdout.decode().strip()


def main(binary):
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        speech = root / "phrase.aiff"
        encoded = root / "phrase.wav"
        subprocess.run(["say", "-v", "Samantha", "-o", speech,
                        "Please repeat the words purple umbrella."], check=True, timeout=20)
        subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-i", speech,
                        "-ac", "1", "-ar", "24000", "-c:a", "pcm_s16le", encoded],
                       check=True, timeout=20)
        with wave.open(str(encoded)) as wav:
            pcm = wav.readframes(wav.getnframes())
        if not pcm:
            raise RuntimeError("local speech synthesis returned no audio")
        input_text = transcribe(binary, pcm)
        # Supply one second of lead-in and seven seconds of room for the spoken reply.
        timeline = bytes(48000) + pcm + bytes(7 * 48000)
        result = subprocess.run([binary, "voice", "live"], input=timeline,
                                stdout=subprocess.PIPE, timeout=35, check=True)
        output = result.stdout
        if not 0 < len(output) <= 30 * 48000 or len(output) % 2:
            raise RuntimeError("missing or invalid reply PCM")
        samples = array.array("h", output)
        if sys.byteorder != "little":
            samples.byteswap()
        peak = max(map(abs, samples))
        if peak == 0:
            raise RuntimeError("reply contained only silence")
        output_text = transcribe(binary, output)
        print(json.dumps({"input_bytes": len(pcm), "input_transcript": input_text,
                          "output_bytes": len(output), "output_transcript": output_text,
                          "peak_pcm16": peak}, ensure_ascii=False))
        if any("purple umbrella" not in s.lower() for s in (input_text, output_text)):
            raise SystemExit("expected phrase was not recognized in both directions")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", help="path to the built swarm binary")
    main(parser.parse_args().binary)
