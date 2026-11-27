const settingKey = "aether.mailSound"
const lockName = "aether-mail-sound"
const quietMs = 4000
const notes = [
  { frequency: 587.33, at: 0 },
  { frequency: 783.99, at: 0.16 },
]

const listeners = new Set<() => void>()
let context: AudioContext | undefined
let remembered: string | null = null
let quietUntil = 0

function audio() {
  if (typeof AudioContext === "undefined") return undefined
  try {
    context ??= new AudioContext()
    void context.resume().catch(() => {})
  } catch {
    return undefined
  }
  return context
}

function ring(ctx: AudioContext) {
  const start = ctx.currentTime + 0.02
  for (const note of notes) {
    const oscillator = ctx.createOscillator()
    const gain = ctx.createGain()
    const at = start + note.at
    oscillator.frequency.value = note.frequency
    gain.gain.setValueAtTime(0.0001, at)
    gain.gain.exponentialRampToValueAtTime(0.2, at + 0.015)
    gain.gain.exponentialRampToValueAtTime(0.0001, at + 0.7)
    oscillator.connect(gain).connect(ctx.destination)
    oscillator.start(at)
    oscillator.stop(at + 0.75)
  }
}

export function mailSoundEnabled() {
  try {
    return (localStorage.getItem(settingKey) ?? remembered) !== "off"
  } catch {
    return remembered !== "off"
  }
}

export function subscribeMailSound(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function setMailSound(on: boolean) {
  remembered = on ? "on" : "off"
  try {
    localStorage.setItem(settingKey, remembered)
  } catch {}
  if (on) audio()
  listeners.forEach((listener) => listener())
}

export function unlockMailSound() {
  if (context?.state === "running" || navigator.userActivation?.hasBeenActive === false || !mailSoundEnabled()) return
  audio()
}

export function testMailSound() {
  const ctx = audio()
  if (ctx) ring(ctx)
}

export function chimeForNewMail() {
  const ctx = context
  if (ctx?.state !== "running" || !mailSoundEnabled()) return
  if (!navigator.locks) {
    if (Date.now() < quietUntil) return
    quietUntil = Date.now() + quietMs
    ring(ctx)
    return
  }
  void navigator.locks
    .request(lockName, { ifAvailable: true }, (lock) => {
      if (!lock) return
      ring(ctx)
      return new Promise((release) => setTimeout(release, quietMs))
    })
    .catch(() => {})
}
