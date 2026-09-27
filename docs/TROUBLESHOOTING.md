# Troubleshooting

## Workers are unreachable on the LAN

1. Confirm all nodes are on the same reachable LAN/subnet.
2. Temporarily disconnect VPN software or enable its local-network/LAN-access option. VPN routing/firewall policies can block private addresses such as `10.x.x.x` or `192.168.x.x` even when Internet access still works.
3. On multi-interface nodes, confirm the Agent is advertising the intended physical LAN address; use `--preferred-address <NODE_LAN_IP>` on Agent startup when auto-selection is ambiguous.
4. Confirm Controller ports used by the current configuration are reachable. The public Quickstart defaults are TCP `8080` (pairing bootstrap), `8443` (secure Agent control), and `9443` (authenticated reverse relay). Allow them only on the intended trusted LAN/private interface; do not expose them to the public Internet.
5. Run `nibia nodes` from the Primary and inspect READY/OFFLINE state and LAST SEEN.

## macOS worker goes to sleep

Run `nibia doctor --local`. v0.7.0-alpha verifies a real `PreventUserIdleSystemSleep` assertion before declaring macOS Power Guard available. During a selected workload, `pmset -g assertions` should show a `caffeinate` owner. NIBIA does not permanently modify the user's `pmset` configuration.

Closing a MacBook lid is a separate OS behavior from Idle Sleep and is not overridden by this Experimental Alpha.

## Why RAM AVAILABLE is larger than SAFE USABLE

`RAM AVAILABLE` is current OS-available memory. `SAFE USABLE` subtracts NIBIA's default automatic reserve: 10% of physical RAM, with a 512 MiB minimum and a 4 GiB maximum. Workload-specific `--memory-reserve` overrides can change the final plan.

NIBIA uses the minimum safe fabric by default; it does not activate every worker merely because the worker is available.

## A node sleeps or disappears during a long workload

Keep laptops connected to external power when possible and configure the OS to avoid automatic sleep while participating in NIBIA. NIBIA Power Guard attempts to keep selected nodes awake during active workloads, but it cannot override every host policy (for example critical battery actions, lid-close sleep, manual sleep, firmware/Modern Standby, or administrator-enforced policy). SERVE can recover automatically when a selected Agent reconnects within the configured recovery window.
