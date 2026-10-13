#!/usr/bin/env python
# -*- coding: utf-8 -*-
import fcntl, os, termios, time, sys

TTY = '/dev/tty1'
VCS = '/dev/vcs1'

def inject(s):
    fd = os.open(TTY, os.O_RDWR)
    for ch in s:
        fcntl.ioctl(fd, termios.TIOCSTI, ch.encode() if isinstance(ch, str) else ch)
        time.sleep(0.08)
    os.close(fd)

def inject_ctrlc():
    fd = os.open(TTY, os.O_RDWR)
    fcntl.ioctl(fd, termios.TIOCSTI, b'\x03')
    os.close(fd)

def screen():
    try:
        with open(VCS, 'rb') as f:
            data = f.read()
        # vcs1 has no header on 2.6+; dimensions 80x25
        text = data.decode('utf-8', errors='replace')
        # collapse blank lines
        lines = [l.rstrip() for l in [text[i:i+80] for i in range(0, len(text), 80)]]
        return '\n'.join([l for l in lines if l])
    except Exception as e:
        return 'READ_ERR: %s' % e

def wait_screen(keyword, timeout=8):
    for _ in range(timeout*5):
        s = screen()
        if keyword in s:
            return True, s
        time.sleep(0.2)
    return False, screen()

def check(name, ok, shot=''):
    print(('PASS' if ok else 'FAIL') + ' - ' + name)
    if not ok:
        print('--- screen ---')
        print(shot[-600:])
        print('--------------')

# back to menu: press Enter a few times to dismiss any pause
def back_menu():
    inject('\r\r\r')
    time.sleep(0.5)

print('=== cetnos-tty functional test ===')
time.sleep(1)

# 1. menu visible
ok, shot = wait_screen('SSCLOUD')
check('menu renders', ok, shot)

# 2. option 3: show disks
inject('3\r')
ok, shot = wait_screen('DATA DISKS')
check('opt3 data disks', ok, shot)
back_menu()
ok, shot = wait_screen('SSCLOUD')
check('opt3 back to menu', ok, shot)

# 3. option 4: ping
inject('4\r')
ok, shot = wait_screen('PING TEST')
check('opt4 ping test', ok, shot)
ok, shot = wait_screen('Press Enter', timeout=15)
check('opt4 ping finish', ok, shot)
back_menu()
ok, shot = wait_screen('SSCLOUD')
check('opt4 back to menu', ok, shot)

# 4. option 1 static, Ctrl+C back
inject('1\r')
ok, shot = wait_screen('NETWORK CONFIG (static)')
check('opt1 static enter', ok, shot)
inject_ctrlc()
time.sleep(1)
ok, shot = wait_screen('SSCLOUD', timeout=5)
check('opt1 ctrl+c back to menu', ok, shot)
# ensure no leftover pause prompt
ok2 = 'Press Enter' not in shot
check('opt1 no pause prompt', ok2, shot)

# 5. option 2 dhcp, Ctrl+C back
inject('2\r')
ok, shot = wait_screen('NETWORK CONFIG (dhcp)')
check('opt2 dhcp enter', ok, shot)
inject_ctrlc()
time.sleep(1)
ok, shot = wait_screen('SSCLOUD', timeout=5)
check('opt2 ctrl+c back to menu', ok, shot)
ok2 = 'Press Enter' not in shot
check('opt2 no pause prompt', ok2, shot)

# 6. invalid choice
inject('9\r')
time.sleep(1)
ok, shot = wait_screen('SSCLOUD')
check('invalid choice stays menu', ok, shot)

# 7. ESC returns to menu
inject('1\r')
wait_screen('NETWORK CONFIG (static)')
inject('\x1b')
time.sleep(1)
ok, shot = wait_screen('SSCLOUD', timeout=5)
check('opt1 esc back to menu', ok, shot)

print('=== done ===')
