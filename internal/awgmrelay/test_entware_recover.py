import contextlib,io,json,os,unittest,urllib.parse
from test_install_mode import load_relay

class RecoveryTest(unittest.TestCase):
 def scenario(self, failure=None):
  r=load_relay();files={};calls=[];core=False
  before='Package: busybox\nVersion: 1.37.0-6\nStatus: install user installed\n\n'
  after=before.replace('install user','install hold')
  def request(op,cfg,method,path,body=None):
   nonlocal core
   calls.append((method,path,body))
   route=path.split('?')[0];p=urllib.parse.parse_qs(urllib.parse.urlsplit(path).query).get('path',[''])[0]
   if route.endswith('/write'):files[body['path']]=body['content'];return {'data':{'ok':True}}
   if route.endswith('/read'):
    if p=='/opt/lib/opkg/status':return {'data':{'content':after if core else before}}
    return {'data':{'content':files[p]}}
   if route.endswith('/list'):
    names=['busybox']+(['df','sh','ash','rm','sleep'] if core else []) if p=='/opt/bin' else [os.path.basename(f) for f in files]
    return {'data':{'entries':[{'name':n,'mode':'-rwxr-xr-x'} for n in names]}}
   if route.endswith('/script-action'):
    core=True
    return {'data':{'ok':True,'running':failure=='running','output':'BUSYBOX_HOLD_CONFIRMED\nRESTORED_SHELL_OK\nLINKS_CREATED=12\n'+('' if failure=='marker' else 'RECOVERY_CONFIRMED\n')+('PRESERVED_BROKEN_APPLET=[\n' if failure=='warning' else '')}}
   if route.endswith('/script-status'):return {'data':{'running':failure=='running'}}
   if route.endswith('/remove'):
    if failure!='cleanup':files.pop(body['path'],None)
    return {'data':{'ok':True}}
   return {'data':{}}
  r.request=request;r.opener=lambda:(object(),object());r.login_if_needed=lambda *a:None
  cfg={'mode':'entware_recover','base_url':'https://router.example.com','recovery_script':'#!/opt/bin/busybox sh\nprintf safe\n','recovery_nonce':'abcdef12'}
  out=io.StringIO();err=None
  try:
   with contextlib.redirect_stdout(out):r.run_entware_recovery(cfg)
  except Exception as e:err=e
  return err,out.getvalue(),calls,files
 def test_verified_success_never_uses_terminal(self):
  err,out,calls,files=self.scenario();self.assertIsNone(err);self.assertIn('__WG_RECOVERY_RESULT__ abcdef12 ',out);self.assertFalse(files)
  self.assertFalse(any('/terminal/' in c[1] for c in calls))
 def test_http200_is_not_success_without_marker_or_finished(self):
  for failure in ['marker','running','cleanup']:
   with self.subTest(failure=failure):
    err,out,calls,files=self.scenario(failure);self.assertIsNotNone(err);self.assertNotIn('__WG_RECOVERY_RESULT__',out)
    if failure=='running':self.assertFalse(any(c[1].endswith('/remove') for c in calls))
 def test_raw_server_error_and_credentials_never_printed(self):
  r=load_relay();r.opener=lambda:(None,None)
  def fail(*a):raise r.RelayError('HTTP 401: PASSWORD_SECRET https://private.example.com')
  r.login_if_needed=fail
  with self.assertRaises(Exception) as e:r.run_entware_recovery({'recovery_nonce':'abcdef12'})
  self.assertNotIn('PASSWORD_SECRET',str(e.exception));self.assertNotIn('private.example',str(e.exception))

 def test_punctuation_applet_warning_is_not_silently_dropped(self):
  err,out,calls,files=self.scenario('warning');self.assertIsNone(err)
  payload=json.loads(next(l.split(' ',2)[2] for l in out.splitlines() if l.startswith('__WG_RECOVERY_RESULT__')))
  self.assertTrue(payload['warnings'])
