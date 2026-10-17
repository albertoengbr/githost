# Windows service installation

GitHost does not yet embed a native Windows SCM handler (tracked in
docs/status.md). Until that lands, run it as a service through a service
wrapper:

## Option A: NSSM (Non-Sucking Service Manager)

```powershell
PS> choco install nssm
PS> nssm install GitHost "C:\Program Files\GitHost\githost.exe" "serve --config C:\ProgramData\GitHost\githost.yaml"
PS> nssm set GitHost AppStdout C:\ProgramData\GitHost\logs\githost.log
PS> nssm set GitHost AppStderr C:\ProgramData\GitHost\logs\githost.log
PS> nssm start GitHost
```

## Option B: WinSW (Windows Service Wrapper)

Place `githost-service.xml` next to the WinSW executable:

```xml
<service>
  <id>GitHost</id>
  <name>GitHost</name>
  <executable>C:\Program Files\GitHost\githost.exe</executable>
  <arguments>serve --config C:\ProgramData\GitHost\githost.yaml</arguments>
  <log mode="roll"></log>
</service>
```

## Option C: Task Scheduler at boot

```powershell
PS> $action = New-ScheduledTaskAction -Execute "C:\Program Files\GitHost\githost.exe" -Argument "serve --config C:\ProgramData\GitHost\githost.yaml"
PS> Register-ScheduledTask -TaskName "GitHost" -Action $action -Trigger (New-ScheduledTaskTrigger -AtStartup) -User "SYSTEM" -RunLevel Limited
```

Data layout on Windows: keep `githost.yaml` under `C:\ProgramData\GitHost`
and the data directory on the same volume (SQLite dislikes network
filesystems).
