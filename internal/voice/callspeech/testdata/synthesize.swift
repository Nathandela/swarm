import AVFoundation
import Foundation

let voice = AVSpeechSynthesizer()
let utterance = AVSpeechUtterance(string: CommandLine.arguments[1])
utterance.voice = AVSpeechSynthesisVoice(language: "en-US")
var samples = [Float]()
var rate = 0.0
voice.write(utterance) { buffer in
    guard let pcm = buffer as? AVAudioPCMBuffer else { exit(1) }
    let count = Int(pcm.frameLength)
    if count == 0 {
        guard !samples.isEmpty, rate > 0 else { exit(1) }
        let ratio = rate / 24000
        let output = (0..<Int(Double(samples.count - 1) / ratio)).map { index -> Int16 in
            let source = Double(index) * ratio
            let i = Int(source)
            let fraction = Float(source - Double(i))
            let value = samples[i] * (1 - fraction) + samples[i + 1] * fraction
            return Int16(max(-32768, min(32767, value * 32768))).littleEndian
        }
        FileHandle.standardOutput.write(output.withUnsafeBytes { Data($0) })
        exit(0)
    }
    rate = pcm.format.sampleRate
    if let channel = pcm.floatChannelData?[0] {
        samples.append(contentsOf: UnsafeBufferPointer(start: channel, count: count))
    } else if let channel = pcm.int16ChannelData?[0] {
        samples.append(contentsOf: UnsafeBufferPointer(start: channel, count: count).map { Float($0) / 32768 })
    } else {
        exit(2)
    }
}
RunLoop.current.run()
