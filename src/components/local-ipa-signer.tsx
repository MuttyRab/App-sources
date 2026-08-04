'use client'

import * as React from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { InstallQrDialog } from '@/components/install-qr-dialog'
import {
  extractAppMetadata,
  extractCertificateMetadata,
  extractProvisioningMetadata,
  type AppMetadata,
  type CertificateMetadata,
  type ProvisioningMetadata,
} from '@/app-metadata'
import { saveOutput, signIpa } from '@/zsign-api'
import type { OutputFile, ZsignProgress } from '@/types'

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const power = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  return `${(bytes / 1024 ** power).toFixed(power === 0 ? 0 : 1)} ${units[power]}`
}

function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleDateString()
}

function isIosDevice() {
  if (typeof navigator === 'undefined') return false
  return /iPad|iPhone|iPod/.test(navigator.userAgent) ||
    (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
}

function isMobileDevice() {
  if (typeof navigator === 'undefined') return false
  return isIosDevice() || /Android/i.test(navigator.userAgent)
}

function defaultOutputName(file: File, metadata?: AppMetadata | null) {
  const base = metadata?.appName?.trim() || file.name.replace(/\.ipa$/i, '') || 'app'
  return `${base.replace(/[^a-z0-9._-]+/gi, '-')}-signed.ipa`
}

export function LocalIpaSigner() {
  const [ipa, setIpa] = React.useState<File | null>(null)
  const [p12, setP12] = React.useState<File | null>(null)
  const [profiles, setProfiles] = React.useState<File[]>([])
  const [password, setPassword] = React.useState('')
  const [showPassword, setShowPassword] = React.useState(false)
  const [bundleId, setBundleId] = React.useState('')
  const [outputName, setOutputName] = React.useState('signed.ipa')
  const [authorized, setAuthorized] = React.useState(false)

  const [appMetadata, setAppMetadata] = React.useState<AppMetadata | null>(null)
  const [certificateMetadata, setCertificateMetadata] = React.useState<CertificateMetadata | null>(null)
  const [profileMetadata, setProfileMetadata] = React.useState<ProvisioningMetadata[]>([])
  const [inspectMessage, setInspectMessage] = React.useState('')

  const [state, setState] = React.useState<'idle' | 'signing' | 'done' | 'error'>('idle')
  const [progress, setProgress] = React.useState(0)
  const [progressLabel, setProgressLabel] = React.useState('Ready')
  const [logs, setLogs] = React.useState<string[]>([])
  const [error, setError] = React.useState('')
  const [output, setOutput] = React.useState<OutputFile | null>(null)
  const [showInstall, setShowInstall] = React.useState(false)

  const ios = React.useMemo(isIosDevice, [])
  const mobile = React.useMemo(isMobileDevice, [])

  const appendLog = React.useCallback((line: string) => {
    const redacted = password ? line.split(password).join('[password hidden]') : line
    setLogs((current) => [...current.slice(-199), redacted])
  }, [password])

  const handleIpa = async (file: File | null) => {
    setIpa(file)
    setOutput(null)
    setAppMetadata(null)
    setError('')
    setInspectMessage('')
    if (!file) return

    setOutputName(defaultOutputName(file))
    try {
      const metadata = await extractAppMetadata(file)
      setAppMetadata(metadata)
      setBundleId(metadata.bundleId)
      setOutputName(defaultOutputName(file, metadata))
    } catch (nextError) {
      setInspectMessage(nextError instanceof Error ? nextError.message : String(nextError))
    }
  }

  const handleP12 = async (file: File | null) => {
    setP12(file)
    setCertificateMetadata(null)
    setError('')
    if (!file || !password) return
    try {
      setCertificateMetadata(await extractCertificateMetadata(file, password))
    } catch {
      // A wrong or not-yet-entered password is validated again before signing.
    }
  }

  const inspectCertificate = async () => {
    if (!p12 || !password) return
    try {
      setCertificateMetadata(await extractCertificateMetadata(p12, password))
      setError('')
    } catch (nextError) {
      setCertificateMetadata(null)
      setError(nextError instanceof Error ? nextError.message : 'Unable to open the P12/PFX file.')
    }
  }

  const handleProfiles = async (files: File[]) => {
    setProfiles(files)
    setProfileMetadata([])
    setError('')
    if (!files.length) return

    const results = await Promise.allSettled(files.map(extractProvisioningMetadata))
    const valid: ProvisioningMetadata[] = []
    for (const result of results) {
      if (result.status === 'fulfilled') valid.push(result.value)
    }
    setProfileMetadata(valid)
    if (valid.length !== files.length) {
      setInspectMessage('One or more provisioning profiles could not be inspected. Signing may still fail if a profile is invalid.')
    }
  }

  const handleProgress = (next: ZsignProgress) => {
    const value = next.total > 0 ? Math.round((next.completed / next.total) * 100) : 0
    setProgress(Math.min(99, Math.max(0, value)))
    setProgressLabel(next.phase === 'extract' ? 'Extracting IPA' : 'Creating signed IPA')
  }

  const handleSign = async () => {
    setError('')
    setOutput(null)

    if (!authorized) {
      setError('Confirm that you own the app or are authorized to test it.')
      return
    }
    if (!ipa || !p12 || !profiles.length || !password) {
      setError('Select an IPA, P12/PFX, provisioning profile, and enter the certificate password.')
      return
    }

    try {
      await extractCertificateMetadata(p12, password)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : 'The P12/PFX password is incorrect.')
      return
    }

    setState('signing')
    setProgress(2)
    setProgressLabel('Starting local signer')
    setLogs([])
    appendLog('Signing started. The original IPA and signing credentials remain in this browser tab.')

    try {
      const result = await signIpa(
        {
          ipa,
          p12,
          profiles,
          password,
          bundleId: bundleId.trim() || undefined,
          outputName: outputName.trim() || defaultOutputName(ipa, appMetadata),
          force: true,
          zipLevel: mobile ? 1 : 6,
        },
        {
          storageMode: mobile ? 'mobile-native' : 'memory',
          onLog: appendLog,
          onProgress: handleProgress,
        },
      )

      if (result.exitCode !== 0) {
        throw new Error(`The signer exited with code ${result.exitCode}. Review the log below.`)
      }
      const signed = result.outputs.find((file) => file.name.toLowerCase().endsWith('.ipa')) ?? result.outputs[0]
      if (!signed) throw new Error('Signing completed without producing an IPA file.')

      setOutput(signed)
      setState('done')
      setProgress(100)
      setProgressLabel('Signed IPA ready')
      appendLog(`Finished: ${signed.name}`)
    } catch (nextError) {
      const message = nextError instanceof Error ? nextError.message : String(nextError)
      setState('error')
      setProgress(0)
      setProgressLabel('Signing failed')
      setError(message)
      appendLog(`ERROR: ${message}`)
    }
  }

  const clearSensitiveData = () => {
    setIpa(null)
    setP12(null)
    setProfiles([])
    setPassword('')
    setBundleId('')
    setOutputName('signed.ipa')
    setAppMetadata(null)
    setCertificateMetadata(null)
    setProfileMetadata([])
    setOutput(null)
    setLogs([])
    setError('')
    setInspectMessage('')
    setState('idle')
    setProgress(0)
    setProgressLabel('Ready')
    setAuthorized(false)
  }

  const canSign = Boolean(ipa && p12 && profiles.length && password && authorized && state !== 'signing')

  return (
    <main className="min-h-screen bg-background text-foreground">
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 px-4 py-8 sm:px-6 sm:py-12">
        <header className="space-y-3 text-center">
          <div className="mx-auto inline-flex items-center rounded-full border border-border bg-card px-3 py-1 text-xs font-medium text-muted-foreground">
            Local processing · No credential uploads
          </div>
          <h1 className="text-3xl font-bold tracking-tight sm:text-4xl">Local IPA Signer</h1>
          <p className="mx-auto max-w-2xl text-sm leading-6 text-muted-foreground sm:text-base">
            Re-sign an IPA with your Apple certificate and provisioning profile. Your original IPA,
            P12/PFX, password, and profile stay in this browser tab.
          </p>
        </header>

        <section className="rounded-2xl border border-border bg-card p-5 shadow-sm sm:p-6">
          <div className="mb-5">
            <h2 className="text-lg font-semibold">1. Choose the files</h2>
            <p className="text-sm text-muted-foreground">All four fields are required.</p>
          </div>

          <div className="grid gap-5">
            <div className="grid gap-2">
              <Label htmlFor="ipa">IPA file</Label>
              <Input
                id="ipa"
                type="file"
                accept=".ipa,application/octet-stream"
                onChange={(event) => void handleIpa(event.target.files?.[0] ?? null)}
                disabled={state === 'signing'}
              />
              {ipa ? <p className="text-xs text-muted-foreground">{ipa.name} · {formatBytes(ipa.size)}</p> : null}
            </div>

            <div className="grid gap-2">
              <Label htmlFor="p12">Apple certificate (.p12 or .pfx)</Label>
              <Input
                id="p12"
                type="file"
                accept=".p12,.pfx,application/x-pkcs12"
                onChange={(event) => void handleP12(event.target.files?.[0] ?? null)}
                disabled={state === 'signing'}
              />
            </div>

            <div className="grid gap-2">
              <Label htmlFor="password">Certificate password</Label>
              <div className="flex gap-2">
                <Input
                  id="password"
                  type={showPassword ? 'text' : 'password'}
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  onBlur={() => void inspectCertificate()}
                  autoComplete="off"
                  spellCheck={false}
                  disabled={state === 'signing'}
                />
                <Button type="button" variant="outline" onClick={() => setShowPassword((value) => !value)}>
                  {showPassword ? 'Hide' : 'Show'}
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">The password is kept only in memory and is never saved.</p>
            </div>

            <div className="grid gap-2">
              <Label htmlFor="profiles">Provisioning profile (.mobileprovision)</Label>
              <Input
                id="profiles"
                type="file"
                accept=".mobileprovision,application/octet-stream"
                multiple
                onChange={(event) => void handleProfiles(Array.from(event.target.files ?? []))}
                disabled={state === 'signing'}
              />
              {profiles.length ? <p className="text-xs text-muted-foreground">{profiles.length} profile{profiles.length === 1 ? '' : 's'} selected</p> : null}
            </div>
          </div>
        </section>

        <section className="rounded-2xl border border-border bg-card p-5 shadow-sm sm:p-6">
          <div className="mb-5">
            <h2 className="text-lg font-semibold">2. Review signing details</h2>
            <p className="text-sm text-muted-foreground">The bundle identifier must be allowed by the selected profile.</p>
          </div>

          <div className="grid gap-5">
            {appMetadata ? (
              <div className="flex items-center gap-4 rounded-xl border border-border bg-background/50 p-4">
                {appMetadata.iconDataUrl ? (
                  <img src={appMetadata.iconDataUrl} alt="" className="h-14 w-14 rounded-xl" />
                ) : (
                  <div className="grid h-14 w-14 place-items-center rounded-xl bg-muted text-xl font-bold">A</div>
                )}
                <div className="min-w-0">
                  <p className="truncate font-semibold">{appMetadata.appName}</p>
                  <p className="truncate text-xs text-muted-foreground">{appMetadata.bundleId}</p>
                  <p className="text-xs text-muted-foreground">Version {appMetadata.version}</p>
                </div>
              </div>
            ) : null}

            <div className="grid gap-2">
              <Label htmlFor="bundleId">Bundle identifier</Label>
              <Input id="bundleId" value={bundleId} onChange={(event) => setBundleId(event.target.value)} disabled={state === 'signing'} />
            </div>

            <div className="grid gap-2">
              <Label htmlFor="outputName">Output file name</Label>
              <Input id="outputName" value={outputName} onChange={(event) => setOutputName(event.target.value)} disabled={state === 'signing'} />
            </div>

            <div className="grid gap-2 text-sm">
              {certificateMetadata ? (
                <div className="rounded-lg border border-border px-3 py-2">
                  <span className="font-medium">Certificate:</span> {certificateMetadata.name} · expires {formatDate(certificateMetadata.expiresAt)}
                </div>
              ) : null}
              {profileMetadata.map((profile, index) => (
                <div key={`${profile.name}-${index}`} className="rounded-lg border border-border px-3 py-2">
                  <span className="font-medium">Profile:</span> {profile.name} · expires {formatDate(profile.expiresAt)}
                </div>
              ))}
            </div>

            {inspectMessage ? <p className="rounded-lg border border-yellow-500/30 bg-yellow-500/10 p-3 text-sm">{inspectMessage}</p> : null}

            <label className="flex cursor-pointer items-start gap-3 rounded-xl border border-border p-4 text-sm">
              <input
                type="checkbox"
                checked={authorized}
                onChange={(event) => setAuthorized(event.target.checked)}
                className="mt-1 h-4 w-4"
              />
              <span>I own this app or I am authorized to sign and test it on the devices included in the provisioning profile.</span>
            </label>
          </div>
        </section>

        <section className="rounded-2xl border border-border bg-card p-5 shadow-sm sm:p-6">
          <div className="mb-5">
            <h2 className="text-lg font-semibold">3. Sign and install</h2>
            <p className="text-sm text-muted-foreground">
              {mobile ? 'Mobile compatibility mode will be used automatically.' : 'Keep this tab open during signing.'}
            </p>
          </div>

          {error ? <div role="alert" className="mb-4 rounded-xl border border-destructive/40 bg-destructive/10 p-4 text-sm text-destructive">{error}</div> : null}

          <div className="mb-4 space-y-2">
            <div className="flex items-center justify-between text-xs text-muted-foreground">
              <span>{progressLabel}</span>
              <span>{progress}%</span>
            </div>
            <div className="h-2 overflow-hidden rounded-full bg-muted">
              <div className="h-full bg-primary transition-[width] duration-300" style={{ width: `${progress}%` }} />
            </div>
          </div>

          <div className="flex flex-col gap-3 sm:flex-row">
            <Button className="flex-1" disabled={!canSign} onClick={() => void handleSign()}>
              {state === 'signing' ? 'Signing…' : 'Sign IPA locally'}
            </Button>
            <Button variant="outline" onClick={clearSensitiveData} disabled={state === 'signing'}>
              Clear sensitive data
            </Button>
          </div>

          {output ? (
            <div className="mt-5 grid gap-3 rounded-xl border border-green-500/30 bg-green-500/10 p-4">
              <div>
                <p className="font-semibold">Signed IPA ready</p>
                <p className="text-xs text-muted-foreground">{output.name} · {formatBytes(output.data instanceof Blob ? output.data.size : output.data.byteLength)}</p>
              </div>
              <div className="flex flex-col gap-2 sm:flex-row">
                <Button onClick={() => saveOutput(output)}>Download signed IPA</Button>
                <Button variant="outline" onClick={() => setShowInstall(true)}>
                  {ios ? 'Install on this iPhone' : 'Create iPhone install link'}
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                Installation temporarily uploads only the already-signed IPA to public HTTPS hosting after you confirm. Your original IPA and credentials are never uploaded.
              </p>
            </div>
          ) : null}

          {logs.length ? (
            <details className="mt-5 rounded-xl border border-border bg-black/90 text-white">
              <summary className="cursor-pointer px-4 py-3 text-sm font-medium">Signing log</summary>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words border-t border-white/10 p-4 text-xs leading-5">{logs.join('\n')}</pre>
            </details>
          ) : null}
        </section>

        <footer className="space-y-2 text-center text-xs leading-5 text-muted-foreground">
          <p>For apps you own or are authorized to test. A matching, unexpired certificate and provisioning profile are still required by Apple.</p>
          <p>Based on the MIT-licensed SylvaSigner project and the zsign WebAssembly runtime. License notices remain included in the source repository.</p>
        </footer>
      </div>

      {showInstall && output ? (
        <InstallQrDialog
          output={output}
          initialMetadata={{
            appName: appMetadata?.appName,
            bundleId: bundleId || appMetadata?.bundleId,
            version: appMetadata?.version,
          }}
          directInstall={ios}
          onClose={() => setShowInstall(false)}
          onLog={appendLog}
        />
      ) : null}
    </main>
  )
}
