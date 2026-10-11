# import subprocess
# from sqlalchemy import text


# def exec_command(cmd:str, decode:bool=False):
#     # result = subprocess.run(cmd.split(), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
#     # if (decode):
#     #     return result.stdout.decode('utf-8')
#     # else:
#     #     return result.stdout
#     return subprocess.run(cmd.split(), capture_output=True, text=True).stdout


# def exec_sp(session, sp:str, data=None, read:bool=True):
#     sess = session()
#     result = None
#     try:
#         if (data):
#                 query = text(f"{sp} :d")
#                 if (read):
#                     result = sess.execute(query, {"d": data}).all()
#                 else:
#                     sess.execute(query, {"d": data})
#                     sess.commit()    
#         else:
#                 result = sess.execute(text(sp)).all()
#     except Exception as e:
#         print(e)
#     finally:
#         sess.close()
#         return result

# def run_speedtest(cmd:str, session):
#     result = exec_command(cmd)
#     exec_sp(session, "spSpeedtests_Insert", result, False)