import fcntl, os, termios, sys, time

def inject(tty, data):
    fd = os.open('/dev/' + tty, os.O_RDWR)
    for c in data:
        ch = c if isinstance(c, str) else chr(c)
        fcntl.ioctl(fd, termios.TIOCSTI, ch.encode() if isinstance(ch, str) else ch)
        time.sleep(0.2)
    os.close(fd)

def read_screen(tty, n=6):
    vcs = '/dev/vcs' + tty.replace('tty', '')
    with open(vcs, 'rb') as f:
        data = f.read()
    width = 80
    lines = [data[i:i+width] for i in range(0, len(data), width)]
    return b'\n'.join(l.rstrip() for l in lines if l.strip()).decode('utf-8', 'replace')

if __name__ == '__main__':
    tty = sys.argv[1] if len(sys.argv) > 1 else 'tty1'
    keys = sys.argv[2].encode().decode('unicode_escape').encode() if len(sys.argv) > 2 else b'666\r'
    print('injecting', repr(keys), 'to', tty)
    inject(tty, keys)
    time.sleep(4)
    print('--- screen ---')
    print(read_screen(tty))
