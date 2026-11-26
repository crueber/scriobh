import sys, random
sys.argv = [sys.argv[0], '/dev/null'] + sys.argv[1:]
src = open('tools/mk.py').read().split("out = sys.argv[1]")[0]
exec(src)
random.seed(3)
words = "the road ran north out of village and nobody she knew had ever followed it past mill said nothing for a long while fire burned down to line of orange along bars morning snow covered tracks last argument turning back".split()
def para(n): return ' '.join(random.choice(words) for _ in range(n)).capitalize() + '.'
kind = sys.argv[2]; outp = sys.argv[3]
ps = []
for c in range(1, 61):
    if kind == 'h1': ps.append(P(f'Chapter {c}', 'Heading1'))
    elif kind == 'plain': ps.append(P(f'Chapter {c}', b=True, center=True, pbb=True))
    else: pass
    for i in range(25):
        ps.append(P(para(100)))
        if i % 8 == 7: ps.append(P('* * *', center=True))
doc(outp, ps)
