# Optional vendor MIB provisioning

Public Serverdesk release archives do not include vendor MIB files. If the customer has the
applicable redistribution/use entitlement, an administrator may copy the licensed `.mib`/`.txt`
files to the installed `mibs` directory (`/opt/serverdesk/mibs` or `C:\serverdesk\mibs`).

Obtain MIBs matching the installed Stratus release only through an authorized customer/vendor
channel and review the applicable license before provisioning them. `config.example.json` uses
`"mib_dir": "mibs"`. Serverdesk continues receiving traps and records unknown objects as numeric
OIDs when no matching licensed MIB is installed.
